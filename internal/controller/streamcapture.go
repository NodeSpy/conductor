package controller

import (
	"bytes"
	"sync"
)

// streamCapture is a turn's output sink for a JSON-lines transcript (claude
// -p --output-format stream-json). The transcript is the live record — the
// tool-call hooks put every action in the audit trail and `conductor watch`
// — so only what the turn's reply is read from is kept: the LAST
// `"type":"result"` line, plus non-JSON diagnostics (bounded). A session of
// any length therefore always keeps its answer.
type streamCapture struct {
	mu     sync.Mutex
	part   []byte
	result []byte
	noise  boundedBuffer
}

func (s *streamCapture) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.part = append(s.part, p...)
	for {
		i := bytes.IndexByte(s.part, '\n')
		if i < 0 {
			if len(s.part) > 8<<20 {
				// A single line this long is not a result envelope worth
				// keeping; drop it rather than grow without bound.
				s.part = s.part[:0]
			}
			break
		}
		s.line(s.part[:i])
		s.part = s.part[i+1:]
	}
	return len(p), nil
}

func (s *streamCapture) line(l []byte) {
	t := bytes.TrimSpace(l)
	if len(t) == 0 {
		return
	}
	if t[0] == '{' {
		if bytes.Contains(t, []byte(`"type":"result"`)) {
			s.result = append(s.result[:0], t...)
		}
		return
	}
	_, _ = s.noise.Write(append(append([]byte(nil), t...), '\n'))
}

func (s *streamCapture) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.part) > 0 {
		s.line(s.part)
		s.part = nil
	}
	out := s.noise.String()
	if len(s.result) > 0 {
		out += string(s.result)
	}
	return out
}
