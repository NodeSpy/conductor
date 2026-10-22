package connector

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/NodeSpy/conductor/internal/core"
)

// File is one file an event carried, fetched through its connector's
// declared `attachments` semantic (plugin-contract.md §2.2).
type File struct {
	Name string
	Data []byte
}

// ErrFilesNotFetched marks an event whose files the engine would not fetch:
// its target was not assigned by the platform, so the file list is the
// sender's own, unverified payload.
var ErrFilesNotFetched = errors.New("the event's files were not fetched: its target was not assigned by the platform")

// EventFileCount is how many files t lists under its declared attachments
// (0 when it declares none).
func EventFileCount(t core.Trigger) int {
	sem := t.Semantics().Attachments
	if sem == nil {
		return 0
	}
	raw, _ := core.LookupFact(t.Facts(), sem.List)
	items, _ := raw.([]any)
	return len(items)
}

// FetchAttachments fetches the files t carries, through this instance's
// declared fetch verb, within the receiving bot's caps: at most maxFiles,
// each at most maxFile bytes (a declared size over it is refused before the
// fetch). nil when the event declares no attachments or carries none.
func (in *Instance) FetchAttachments(ctx context.Context, t core.Trigger, maxFiles int, maxFile int64) ([]File, error) {
	sem := t.Semantics().Attachments
	if in == nil || sem == nil || t.Instance != in.Name {
		return nil, nil
	}
	facts := t.Facts()
	raw, _ := core.LookupFact(facts, sem.List)
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil, nil
	}
	if !t.TargetTrusted {
		return nil, ErrFilesNotFetched
	}
	if len(items) > maxFiles {
		return nil, fmt.Errorf("%d files, over the cap of %d per message (attachments.max_files)", len(items), maxFiles)
	}
	var out []File
	for i, x := range items {
		item, _ := x.(map[string]any)
		if item == nil {
			return nil, fmt.Errorf("file %d: not an object", i+1)
		}
		name := ""
		if sem.Name != "" {
			if v, ok := core.LookupFact(item, sem.Name); ok && v != nil {
				name = fmt.Sprint(v)
			}
		}
		if sem.Size != "" {
			if v, ok := core.LookupFact(item, sem.Size); ok {
				if n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64); err == nil && n > maxFile {
					return nil, fmt.Errorf("%q: %d bytes, over the cap of %d per file (attachments.max_file)", name, n, maxFile)
				}
			}
		}
		res, err := in.InvokeFinal(ctx, sem.Fetch, core.DeclaredArgs(sem.Args, item))
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %w", name, err)
		}
		s, _ := res[sem.Content].(string)
		data, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %s is not base64: %w", name, sem.Content, err)
		}
		if int64(len(data)) > maxFile {
			return nil, fmt.Errorf("%q: %d bytes, over the cap of %d per file (attachments.max_file)", name, len(data), maxFile)
		}
		out = append(out, File{Name: name, Data: data})
	}
	return out, nil
}
