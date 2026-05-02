// Package session holds peershd state that is per-PTY-session but
// outside the lifetime of a single QUIC stream.
package session

import "strings"

// CWDTracker keeps OSC parser state across terminal pump chunks.
type CWDTracker struct {
	mode     cwdParserMode
	payload  []byte
	overflow bool
}

type cwdParserMode uint8

const (
	cwdText cwdParserMode = iota
	cwdEscaped
	cwdOSC
	cwdOSCStringEscaped
)

const cwdMaxBody = 4096

// NewCWDTracker returns a fresh tracker.
func NewCWDTracker() *CWDTracker { return &CWDTracker{} }

// Feed returns newly observed OSC 9;9 paths in stream order.
func (t *CWDTracker) Feed(p []byte) []string {
	paths := make([]string, 0, 1)
	for _, b := range p {
		switch t.mode {
		case cwdText:
			if b == 0x1b {
				t.mode = cwdEscaped
			}
		case cwdEscaped:
			t.feedEscaped(b)
		case cwdOSC:
			if b == 0x07 {
				paths = t.finishOSC(paths)
				continue
			}
			if b == 0x1b {
				t.mode = cwdOSCStringEscaped
				continue
			}
			t.appendPayload(b)
		case cwdOSCStringEscaped:
			if b == '\\' {
				paths = t.finishOSC(paths)
				continue
			}
			t.reset()
			t.feedEscaped(b)
		}
	}
	return paths
}

func (t *CWDTracker) feedEscaped(b byte) {
	switch b {
	case ']':
		t.mode = cwdOSC
		t.payload = t.payload[:0]
		t.overflow = false
	case 0x1b:
		t.mode = cwdEscaped
	default:
		t.mode = cwdText
	}
}

func (t *CWDTracker) appendPayload(b byte) {
	if len(t.payload) < cwdMaxBody {
		t.payload = append(t.payload, b)
		return
	}
	t.overflow = true
}

func (t *CWDTracker) finishOSC(paths []string) []string {
	if !t.overflow {
		if path, ok := parseOSC99(t.payload); ok {
			paths = append(paths, path)
		}
	}
	t.reset()
	return paths
}

func (t *CWDTracker) reset() {
	t.mode = cwdText
	t.payload = t.payload[:0]
	t.overflow = false
}

func parseOSC99(body []byte) (string, bool) {
	const prefix = "9;9;"
	s := string(body)
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	payload := strings.TrimSpace(s[len(prefix):])
	payload = strings.Trim(payload, `"`)
	if payload == "" {
		return "", false
	}
	return payload, true
}
