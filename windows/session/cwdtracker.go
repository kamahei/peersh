// Package session holds peershd state that is per-PTY-session but
// outside the lifetime of a single QUIC stream — currently just the
// current-working-directory tracker.
//
// cwdTracker scans terminal output bytes for OSC 9;9 escape sequences
// emitted by the shell prompt wrapper that windows/shell installs at
// startup. Recognised forms (BEL or ST terminator):
//
//	ESC ] 9 ; 9 ; <path> BEL
//	ESC ] 9 ; 9 ; <path> ESC \
//
// State is persisted across pump reads because a single OSC sequence
// may straddle a 16 KiB pump boundary. A 4 KiB body cap prevents a
// runaway sequence from growing memory unbounded.
//
// Lifted from peersh/session/session/cwdtracker.go (MIT) —
// the algorithm is identical because the prompt wrapper that produces
// these sequences is the same.
package session

import "strings"

type CWDTracker struct {
	state         cwdParserState
	body          []byte
	bodyTruncated bool
}

type cwdParserState int

const (
	cwdScan cwdParserState = iota
	cwdAfterEsc
	cwdInOSC
	cwdOSCEsc
)

const cwdMaxBody = 4096

// NewCWDTracker returns a fresh tracker.
func NewCWDTracker() *CWDTracker { return &CWDTracker{} }

// Feed processes a chunk of raw output and returns each newly observed
// CWD path. Most chunks return nil. Path strings are returned in the
// order they appeared in the stream; callers typically only care about
// the last one.
func (t *CWDTracker) Feed(p []byte) []string {
	var out []string
	for _, b := range p {
		switch t.state {
		case cwdScan:
			if b == 0x1b {
				t.state = cwdAfterEsc
			}
		case cwdAfterEsc:
			switch b {
			case ']':
				t.state = cwdInOSC
				t.body = t.body[:0]
			case 0x1b:
				// stay in AfterEsc on consecutive ESCs
			default:
				t.state = cwdScan
			}
		case cwdInOSC:
			switch b {
			case 0x07:
				if !t.bodyTruncated {
					if path, ok := parseOSC99(t.body); ok {
						out = append(out, path)
					}
				}
				t.state = cwdScan
				t.body = t.body[:0]
				t.bodyTruncated = false
			case 0x1b:
				t.state = cwdOSCEsc
			default:
				if len(t.body) < cwdMaxBody {
					t.body = append(t.body, b)
				} else {
					t.bodyTruncated = true
				}
			}
		case cwdOSCEsc:
			if b == '\\' {
				if !t.bodyTruncated {
					if path, ok := parseOSC99(t.body); ok {
						out = append(out, path)
					}
				}
				t.state = cwdScan
				t.body = t.body[:0]
				t.bodyTruncated = false
			} else {
				t.body = t.body[:0]
				t.bodyTruncated = false
				if b == 0x1b {
					t.state = cwdAfterEsc
				} else {
					t.state = cwdScan
				}
			}
		}
	}
	return out
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
