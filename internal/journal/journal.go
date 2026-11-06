// Package journal is conductor's durable event log: one append-only sequence
// of events that every client reads at its own pace (design: event-journal).
//
// Events are JSONL lines in size-rotated segment files under one directory.
// Every event gets the next sequence number; a cursor ("<epoch>-<seq>") names
// a position, and the epoch (a random id stored beside the segments) changes
// only when the journal is recreated, so a client can tell "carry on from
// here" from "this is a different journal, refetch".
//
// Readers pull: Read returns the events after a cursor, from memory when they
// are recent and from the segments otherwise, and Changed wakes a reader when
// something is appended. Nothing is ever dropped for a slow reader; a reader
// whose cursor has aged out of retention, or names another epoch, is told to
// reset instead.
//
// The journal knows nothing about what its events mean: topics, types,
// subjects and payloads are the producers'.
package journal

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event is one journal entry.
type Event struct {
	// ID is the event's cursor ("<epoch>-<seq>"): resume after it to get the
	// events that follow.
	ID      string          `json:"id,omitempty"`
	Seq     int64           `json:"seq"`
	Topic   string          `json:"topic"`
	Type    string          `json:"type"`
	Subject string          `json:"subject,omitempty"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Options configure a journal. Zero values take the defaults.
type Options struct {
	// Dir is where the epoch and segments live. Empty: a memory-only journal
	// (a fresh epoch, nothing persisted, only the newest MemEvents readable).
	Dir          string
	SegmentBytes int64         // rotate the active segment past this size (default 4 MiB)
	MaxAge       time.Duration // delete closed segments older than this (default 7 days)
	MaxBytes     int64         // delete the oldest closed segments past this total (default 128 MiB)
	MemEvents    int           // recent events kept decoded in memory (default 4096)
	Now          func() time.Time
}

func (o *Options) defaults() {
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = 4 << 20
	}
	if o.MaxAge <= 0 {
		o.MaxAge = 7 * 24 * time.Hour
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 128 << 20
	}
	if o.MemEvents <= 0 {
		o.MemEvents = 4096
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Journal is a durable, append-only event log. Safe for concurrent use.
type Journal struct {
	opts  Options
	epoch string

	mu      sync.Mutex
	seq     int64     // last appended
	segs    []segment // oldest first; the last is active
	active  *os.File  // the last segment, open for append
	size    int64     // the active segment's size
	recent  []Event   // the newest events, oldest first, capped at MemEvents
	changed chan struct{}
	closed  bool
}

type segment struct {
	first int64 // the seq of the segment's first event
	path  string
}

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("journal: closed")

const epochFile = "epoch"

// Open opens (or creates) the journal in opts.Dir. A torn final line, left by
// a crash mid-append, is cut off.
func Open(opts Options) (*Journal, error) {
	opts.defaults()
	if opts.Dir == "" {
		epoch, err := newEpoch()
		if err != nil {
			return nil, err
		}
		return &Journal{opts: opts, epoch: epoch, changed: make(chan struct{})}, nil
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	j := &Journal{opts: opts, changed: make(chan struct{})}
	segs, err := listSegments(opts.Dir)
	if err != nil {
		return nil, err
	}
	epoch, err := os.ReadFile(filepath.Join(opts.Dir, epochFile))
	switch {
	case err == nil && len(strings.TrimSpace(string(epoch))) > 0:
		j.epoch = strings.TrimSpace(string(epoch))
	case err == nil || errors.Is(err, os.ErrNotExist):
		// A new journal. Segments without an epoch can't be trusted to belong
		// to any cursor a client holds, so start clean.
		for _, s := range segs {
			_ = os.Remove(s.path)
		}
		segs = nil
		if j.epoch, err = newEpoch(); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(opts.Dir, epochFile), []byte(j.epoch+"\n")); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	j.segs = segs
	if err := j.recover(); err != nil {
		return nil, err
	}
	return j, nil
}

// recover finds the last sequence number, cuts a torn tail, loads the newest
// events into memory and opens the active segment for append.
func (j *Journal) recover() error {
	if len(j.segs) == 0 {
		return j.openSegment(1)
	}
	last := j.segs[len(j.segs)-1]
	evs, good, err := readSegment(last.path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(last.path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	j.active, j.size = f, good
	if n := len(evs); n > 0 {
		j.seq = evs[n-1].Seq
	} else {
		j.seq = last.first - 1
	}
	// Seed memory from the newest segments, newest last.
	var mem []Event
	for i := len(j.segs) - 1; i >= 0 && len(mem) < j.opts.MemEvents; i-- {
		var seg []Event
		if i == len(j.segs)-1 {
			seg = evs
		} else if seg, _, err = readSegment(j.segs[i].path); err != nil {
			break
		}
		mem = append(append([]Event(nil), seg...), mem...)
	}
	if len(mem) > j.opts.MemEvents {
		mem = mem[len(mem)-j.opts.MemEvents:]
	}
	for i := range mem {
		mem[i].ID = j.cursor(mem[i].Seq)
	}
	j.recent = mem
	return nil
}

// Epoch is this journal's identity.
func (j *Journal) Epoch() string { return j.epoch }

// Cursor is the position after the newest event: reading from it returns only
// what is appended later.
func (j *Journal) Cursor() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursor(j.seq)
}

func (j *Journal) cursor(seq int64) string { return j.epoch + "-" + strconv.FormatInt(seq, 10) }

// Append records one event and wakes every reader. data is encoded as JSON.
func (j *Journal) Append(topic, typ, subject string, data any) (Event, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Event{}, fmt.Errorf("journal: encode %s/%s: %w", topic, typ, err)
		}
		raw = b
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Event{}, ErrClosed
	}
	ev := Event{Seq: j.seq + 1, Topic: topic, Type: typ, Subject: subject, TS: j.opts.Now().UTC(), Data: raw}
	line, err := json.Marshal(ev) // ID is derived, never stored
	if err != nil {
		return Event{}, err
	}
	line = append(line, '\n')
	if j.active != nil {
		if j.size > 0 && j.size+int64(len(line)) > j.opts.SegmentBytes {
			if err := j.rotate(ev.Seq); err != nil {
				return Event{}, err
			}
		}
		if _, err := j.active.Write(line); err != nil {
			return Event{}, fmt.Errorf("journal: append: %w", err)
		}
		j.size += int64(len(line))
	}
	j.seq = ev.Seq
	ev.ID = j.cursor(ev.Seq)
	j.recent = append(j.recent, ev)
	if over := len(j.recent) - j.opts.MemEvents; over > 0 {
		j.recent = append([]Event(nil), j.recent[over:]...)
	}
	close(j.changed)
	j.changed = make(chan struct{})
	return ev, nil
}

// Changed returns a channel closed by the next Append (or by Close). Take it
// before reading, then wait on it once the read comes back empty.
func (j *Journal) Changed() <-chan struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.changed
}

// Read returns up to limit events after cursor whose topic is in topics (all
// topics when empty), and the cursor to read from next. The next cursor moves
// past events of other topics too, so a filtered reader doesn't rescan them.
//
// reset is true when cursor can't be continued: it names another epoch, is
// malformed, lies beyond the newest event, or has aged out of retention. The
// events are then empty and next is the journal's current position; the
// reader should refetch whatever state it derives from the journal.
//
// An empty cursor means "from now": no events, no reset.
func (j *Journal) Read(cursor string, topics []string, limit int) (events []Event, next string, reset bool, err error) {
	if limit <= 0 {
		limit = 256
	}
	j.mu.Lock()
	head := j.seq
	if cursor == "" {
		j.mu.Unlock()
		return nil, j.cursor(head), false, nil
	}
	after, ok := j.parse(cursor)
	if !ok || after > head {
		j.mu.Unlock()
		return nil, j.cursor(head), true, nil
	}
	if after == head {
		j.mu.Unlock()
		return nil, cursor, false, nil
	}
	want := topicSet(topics)
	// Recent events: served from memory.
	if n := len(j.recent); n > 0 && j.recent[0].Seq <= after+1 {
		last := after
		for _, ev := range j.recent[after+1-j.recent[0].Seq:] {
			last = ev.Seq
			if want == nil || want[ev.Topic] {
				events = append(events, ev)
				if len(events) == limit {
					break
				}
			}
		}
		j.mu.Unlock()
		return events, j.cursor(last), false, nil
	}
	segs := append([]segment(nil), j.segs...)
	j.mu.Unlock()

	// Older events: scan the segments from the one holding after+1.
	if len(segs) == 0 || segs[0].first > after+1 {
		return nil, j.cursor(head), true, nil
	}
	start := sort.Search(len(segs), func(i int) bool { return segs[i].first > after+1 }) - 1
	last := after
	for _, s := range segs[start:] {
		evs, _, rerr := readSegment(s.path)
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				// Retention removed it mid-read.
				return nil, j.cursor(head), true, nil
			}
			return nil, "", false, rerr
		}
		for _, ev := range evs {
			if ev.Seq <= after || ev.Seq > head {
				continue
			}
			last = ev.Seq
			if want == nil || want[ev.Topic] {
				ev.ID = j.cursor(ev.Seq)
				events = append(events, ev)
				if len(events) == limit {
					return events, j.cursor(last), false, nil
				}
			}
		}
	}
	return events, j.cursor(last), false, nil
}

// parse returns the sequence number a cursor of this epoch names.
func (j *Journal) parse(cursor string) (int64, bool) {
	ep, s, ok := strings.Cut(cursor, "-")
	if !ok || ep != j.epoch {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Close stops appends and wakes every waiting reader.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	close(j.changed)
	j.changed = make(chan struct{})
	close(j.changed) // later Changed calls never block either
	if j.active == nil {
		return nil
	}
	return j.active.Close()
}

// rotate closes the active segment, starts one at seq first and applies
// retention to the closed ones. Called with mu held.
func (j *Journal) rotate(first int64) error {
	if err := j.active.Close(); err != nil {
		return err
	}
	if err := j.openSegment(first); err != nil {
		return err
	}
	j.prune()
	return nil
}

func (j *Journal) openSegment(first int64) error {
	p := filepath.Join(j.opts.Dir, segmentName(first))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	j.segs = append(j.segs, segment{first: first, path: p})
	j.active, j.size = f, 0
	return nil
}

// prune deletes closed segments past MaxAge, then the oldest closed ones while
// the total is over MaxBytes. The active segment is never deleted.
func (j *Journal) prune() {
	cut := j.opts.Now().Add(-j.opts.MaxAge)
	var total int64
	sizes := make([]int64, len(j.segs))
	mtimes := make([]time.Time, len(j.segs))
	for i, s := range j.segs {
		if fi, err := os.Stat(s.path); err == nil {
			sizes[i], mtimes[i] = fi.Size(), fi.ModTime()
			total += fi.Size()
		}
	}
	drop := 0
	for drop < len(j.segs)-1 && (mtimes[drop].Before(cut) || total > j.opts.MaxBytes) {
		total -= sizes[drop]
		_ = os.Remove(j.segs[drop].path)
		drop++
	}
	j.segs = append([]segment(nil), j.segs[drop:]...)
}

func segmentName(first int64) string { return fmt.Sprintf("seg-%020d.jsonl", first) }

func listSegments(dir string) ([]segment, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []segment
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".jsonl"), 10, 64)
		if err != nil || n < 1 {
			continue
		}
		out = append(out, segment{first: n, path: filepath.Join(dir, name)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].first < out[b].first })
	return out, nil
}

// readSegment decodes a segment's events and returns the offset just past the
// last whole, valid line (anything after it is a torn append).
func readSegment(path string) ([]Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var (
		out  []Event
		good int64
	)
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var ev Event
			if jerr := json.Unmarshal(bytes.TrimSpace(line), &ev); jerr != nil || ev.Seq <= 0 {
				return out, good, nil
			}
			out = append(out, ev)
			good += int64(len(line))
		}
		if err == io.EOF {
			return out, good, nil
		}
		if err != nil {
			return nil, 0, err
		}
	}
}

func topicSet(topics []string) map[string]bool {
	if len(topics) == 0 {
		return nil
	}
	m := make(map[string]bool, len(topics))
	for _, t := range topics {
		m[t] = true
	}
	return m
}

func newEpoch() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
