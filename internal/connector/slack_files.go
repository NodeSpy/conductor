package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// slackFileVerbs are the read verbs over a message's thread: `thread`
// (the ordered messages, with author names and a permalink) and `download`
// (the thread's files, staged under conductor's state dir).
func slackFileVerbs() []VerbDecl {
	return []VerbDecl{
		{
			Name: "thread", Desc: "read a message's thread: ordered messages with author names, file metadata, and a permalink",
			Options: Schema{
				"channel": {Type: TString, Required: true, Scope: "channel"},
				"ts":      {Type: TString, Required: true, Desc: "the thread's root ts (or any message in it)"},
				"limit":   {Type: TInt, Desc: fmt.Sprintf("max messages (default %d, at most %d)", defaultThreadLimit, maxThreadLimit)},
			},
			Outputs: Schema{
				"messages":  {Type: TList, Desc: "[{user, user_name, text, ts, files: [{id, name, mimetype, size}]}], oldest first"},
				"text":      {Type: TString, Desc: "the thread as plain text, one message per block"},
				"permalink": {Type: TString},
				"thread_ts": {Type: TString},
				"count":     {Type: TInt},
				"truncated": {Type: TBool, Desc: "the thread had more messages than limit"},
			},
		},
		{
			Name: "download", Desc: "download a message's (or its whole thread's) files into conductor's state dir",
			Options: Schema{
				"channel":         {Type: TString, Required: true, Scope: "channel"},
				"ts":              {Type: TString, Required: true, Desc: "the thread's root ts (or the message's own ts with thread: false)"},
				"thread":          {Type: TBool, Desc: "every file in the thread (default true); false: only the message at ts"},
				"max_files":       {Type: TInt, Desc: fmt.Sprintf("default %d, at most %d", defaultMaxFiles, capMaxFiles)},
				"max_file_bytes":  {Type: TInt, Desc: fmt.Sprintf("default %d, at most %d", defaultMaxFileBytes, capMaxFileBytes)},
				"max_total_bytes": {Type: TInt, Desc: fmt.Sprintf("default %d, at most %d", defaultMaxTotalBytes, capMaxTotalBytes)},
			},
			Outputs: Schema{
				"dir":     {Type: TString, Desc: "the staging directory"},
				"files":   {Type: TList, Desc: "[{name, path, mimetype, size}]"},
				"paths":   {Type: TList},
				"images":  {Type: TList, Desc: "paths of the image/* files (for an agent step's images:)"},
				"skipped": {Type: TList, Desc: "[{name, reason}] files not downloaded"},
				"count":   {Type: TInt},
			},
		},
	}
}

const (
	defaultThreadLimit = 200
	maxThreadLimit     = 1000

	defaultMaxFiles      = 10
	capMaxFiles          = 50
	defaultMaxFileBytes  = 20 << 20
	capMaxFileBytes      = 50 << 20
	defaultMaxTotalBytes = 50 << 20
	capMaxTotalBytes     = 200 << 20

	// stagedFileTTL: staging dirs older than this are pruned on the next
	// download.
	stagedFileTTL = 7 * 24 * time.Hour
)

// slackMessage is the subset of a conversations.replies message read here.
type slackMessage struct {
	User     string `json:"user"`
	BotID    string `json:"bot_id"`
	Username string `json:"username"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	Files    []struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		Mimetype           string `json:"mimetype"`
		Size               int64  `json:"size"`
		Mode               string `json:"mode"`
		URLPrivate         string `json:"url_private"`
		URLPrivateDownload string `json:"url_private_download"`
	} `json:"files"`
}

// get calls a read-style Web API method with query parameters.
func (a *slackAPI) get(ctx context.Context, method string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/"+method+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("slack %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		return fmt.Errorf("slack %s: %s", method, env.Error)
	}
	return json.Unmarshal(raw, out)
}

// replies pages conversations.replies until limit messages or the end.
func (a *slackAPI) replies(ctx context.Context, channel, ts string, limit int) (msgs []slackMessage, truncated bool, err error) {
	cursor := ""
	for page := 0; page < 100; page++ {
		p := url.Values{"channel": {channel}, "ts": {ts}, "limit": {"200"}, "inclusive": {"true"}}
		if cursor != "" {
			p.Set("cursor", cursor)
		}
		var out struct {
			Messages []slackMessage `json:"messages"`
			HasMore  bool           `json:"has_more"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := a.get(ctx, "conversations.replies", p, &out); err != nil {
			return nil, false, err
		}
		for _, m := range out.Messages {
			if len(msgs) >= limit {
				return msgs, true, nil
			}
			msgs = append(msgs, m)
		}
		cursor = out.Meta.NextCursor
		if cursor == "" {
			return msgs, false, nil
		}
	}
	return msgs, true, nil
}

// userNames caches users.info display names for the connector's lifetime.
type userNames struct {
	mu    sync.Mutex
	names map[string]string
}

func (a *slackAPI) userName(ctx context.Context, cache *userNames, id string) string {
	if id == "" {
		return ""
	}
	cache.mu.Lock()
	if n, ok := cache.names[id]; ok {
		cache.mu.Unlock()
		return n
	}
	cache.mu.Unlock()
	var out struct {
		User struct {
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	name := id
	if err := a.get(ctx, "users.info", url.Values{"user": {id}}, &out); err == nil {
		for _, n := range []string{out.User.Profile.DisplayName, out.User.Profile.RealName, out.User.RealName, out.User.Name} {
			if strings.TrimSpace(n) != "" {
				name = strings.TrimSpace(n)
				break
			}
		}
	}
	cache.mu.Lock()
	if cache.names == nil {
		cache.names = map[string]string{}
	}
	cache.names[id] = name
	cache.mu.Unlock()
	return name
}

func (a *slackAPI) permalink(ctx context.Context, channel, ts string) string {
	var out struct {
		Permalink string `json:"permalink"`
	}
	if err := a.get(ctx, "chat.getPermalink", url.Values{"channel": {channel}, "message_ts": {ts}}, &out); err != nil {
		return ""
	}
	return out.Permalink
}

// intOpt reads an integer option with a default and a hard cap.
func intOpt(opts map[string]any, key string, def, max int) int {
	n := def
	switch v := opts[key].(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
	}
	if n <= 0 {
		n = def
	}
	if n > max {
		n = max
	}
	return n
}

func (s *slackImpl) threadVerb(ctx context.Context, opts map[string]any) (map[string]any, error) {
	channel, _ := opts["channel"].(string)
	ts, _ := opts["ts"].(string)
	if channel == "" || ts == "" {
		return nil, fmt.Errorf("slack.thread: options.channel and ts are required")
	}
	msgs, truncated, err := s.api.replies(ctx, channel, ts, intOpt(opts, "limit", defaultThreadLimit, maxThreadLimit))
	if err != nil {
		return nil, fmt.Errorf("slack.thread: %w", err)
	}
	var list []any
	var text strings.Builder
	threadTS := ts
	for i, m := range msgs {
		if i == 0 && m.ThreadTS != "" {
			threadTS = m.ThreadTS
		}
		name := s.api.userName(ctx, &s.names, m.User)
		if name == "" {
			name = firstNonEmptyStr(m.Username, "bot")
		}
		var files []any
		var fileNames []string
		for _, f := range m.Files {
			files = append(files, map[string]any{"id": f.ID, "name": f.Name, "mimetype": f.Mimetype, "size": f.Size})
			fileNames = append(fileNames, f.Name)
		}
		list = append(list, map[string]any{"user": m.User, "user_name": name, "text": m.Text, "ts": m.TS, "files": files})
		fmt.Fprintf(&text, "%s (%s):\n%s\n", name, m.TS, m.Text)
		if len(fileNames) > 0 {
			fmt.Fprintf(&text, "[files: %s]\n", strings.Join(fileNames, ", "))
		}
		text.WriteString("\n")
	}
	return map[string]any{
		"messages": list, "text": strings.TrimRight(text.String(), "\n"), "count": len(list),
		"permalink": s.api.permalink(ctx, channel, ts), "thread_ts": threadTS, "truncated": truncated,
	}, nil
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// unsafeNameRe matches every character a staged file name may not keep.
var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeFileName reduces a Slack-supplied file name to a safe base name:
// no directory components, no characters outside [A-Za-z0-9._-] (so no
// template braces, quotes, or spaces either), no leading dots/dashes, at
// most 100 bytes with the extension kept.
func sanitizeFileName(name, fallback string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base("/" + name)
	name = unsafeNameRe.ReplaceAllString(name, "_")
	name = strings.TrimLeft(name, ".-_")
	if len(name) > 100 {
		ext := filepath.Ext(name)
		if len(ext) > 10 {
			ext = ""
		}
		name = name[:100-len(ext)] + ext
	}
	if name == "" || name == "." {
		name = unsafeNameRe.ReplaceAllString(fallback, "_")
	}
	if name == "" {
		name = "file"
	}
	return name
}

// stagingRoot is where downloaded Slack files land (overridable in tests).
var stagingRoot = func() string { return filepath.Join(config.StateDir(), "slack-files") }

// fileURLAllowed reports whether a file URL may receive the bot token: an
// https URL on slack.com, or the configured API base host (hermetic tests).
func (a *slackAPI) fileURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if base, err := url.Parse(a.base); err == nil && base.Host != "" && u.Host == base.Host && u.Scheme == base.Scheme {
		return true
	}
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && (host == "slack.com" || strings.HasSuffix(host, ".slack.com"))
}

var errTooLarge = errors.New("larger than max_file_bytes")

// fetchFile downloads one private file to path, refusing more than max bytes.
func (a *slackAPI) fetchFile(ctx context.Context, rawURL, path string, max int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	client := &http.Client{Timeout: 2 * time.Minute, Transport: a.httpc.Transport,
		// Never carry the bot token off Slack: a redirect must stay on an
		// allowed host (Go drops Authorization cross-host anyway).
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 5 || !a.fileURLAllowed(r.URL.String()) {
				return fmt.Errorf("redirect to %s refused", r.URL.Host)
			}
			return nil
		}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		// Slack answers a token without files:read with its HTML login page.
		return 0, fmt.Errorf("got an HTML page instead of the file (does the bot token have files:read?)")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = errTooLarge
	}
	if err != nil {
		_ = os.Remove(path)
		return 0, err
	}
	return n, nil
}

func isImage(mime string) bool { return strings.HasPrefix(strings.ToLower(mime), "image/") }

func (s *slackImpl) downloadVerb(ctx context.Context, opts map[string]any) (map[string]any, error) {
	channel, _ := opts["channel"].(string)
	ts, _ := opts["ts"].(string)
	if channel == "" || ts == "" {
		return nil, fmt.Errorf("slack.download: options.channel and ts are required")
	}
	wholeThread := true
	if v, ok := opts["thread"].(bool); ok {
		wholeThread = v
	}
	maxFiles := intOpt(opts, "max_files", defaultMaxFiles, capMaxFiles)
	maxFile := int64(intOpt(opts, "max_file_bytes", defaultMaxFileBytes, capMaxFileBytes))
	maxTotal := int64(intOpt(opts, "max_total_bytes", defaultMaxTotalBytes, capMaxTotalBytes))

	msgs, _, err := s.api.replies(ctx, channel, ts, maxThreadLimit)
	if err != nil {
		return nil, fmt.Errorf("slack.download: %w", err)
	}
	if !wholeThread {
		var only []slackMessage
		for _, m := range msgs {
			if m.TS == ts {
				only = append(only, m)
			}
		}
		msgs = only
	}

	root := stagingRoot()
	dir := filepath.Join(root, sanitizeFileName(channel, "channel")+"-"+sanitizeFileName(ts, "ts"))
	if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("slack.download: bad staging dir for %s/%s", channel, ts)
	}
	pruneStaging(root, dir)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("slack.download: reset %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("slack.download: %w", err)
	}

	files, paths, images, skipped := []any{}, []any{}, []any{}, []any{}
	var total int64
	n := 0
	for _, m := range msgs {
		for _, f := range m.Files {
			skip := func(reason string) {
				skipped = append(skipped, map[string]any{"name": f.Name, "reason": reason})
			}
			src := firstNonEmptyStr(f.URLPrivateDownload, f.URLPrivate)
			switch {
			case f.Mode == "external" || f.Mode == "tombstone" || f.Mode == "hidden_by_limit" || src == "":
				skip("not downloadable (" + firstNonEmptyStr(f.Mode, "no url") + ")")
				continue
			case n >= maxFiles:
				skip("max_files reached")
				continue
			case f.Size > maxFile:
				skip("larger than max_file_bytes")
				continue
			case total+f.Size > maxTotal:
				skip("max_total_bytes reached")
				continue
			case !s.api.fileURLAllowed(src):
				skip("file URL is not on slack.com")
				continue
			}
			name := fmt.Sprintf("%02d-%s", n+1, sanitizeFileName(f.Name, f.ID))
			path := filepath.Join(dir, name)
			if rel, err := filepath.Rel(dir, path); err != nil || strings.Contains(rel, string(filepath.Separator)) || strings.HasPrefix(rel, "..") {
				skip("unsafe name")
				continue
			}
			limit := maxFile
			if rest := maxTotal - total; rest < limit {
				limit = rest
			}
			size, err := s.api.fetchFile(ctx, src, path, limit)
			if err != nil {
				skip(err.Error())
				continue
			}
			n++
			total += size
			files = append(files, map[string]any{"name": name, "path": path, "mimetype": f.Mimetype, "size": size})
			paths = append(paths, path)
			if isImage(f.Mimetype) {
				images = append(images, path)
			}
		}
	}
	return map[string]any{"dir": dir, "files": files, "paths": paths, "images": images, "skipped": skipped, "count": n}, nil
}

// pruneStaging removes staging dirs under root older than stagedFileTTL
// (best effort), except keep.
func pruneStaging(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stagedFileTTL)
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if !e.IsDir() || p == keep {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(p)
		}
	}
}
