package session_test

import (
	"testing"

	"github.com/peersh/peersh/windows/session"
)

func TestCWDTrackerSingleBELTerminated(t *testing.T) {
	tr := session.NewCWDTracker()
	got := tr.Feed([]byte("\x1b]9;9;C:\\Users\\alice\x07PS> "))
	if len(got) != 1 || got[0] != `C:\Users\alice` {
		t.Fatalf("expected single BEL-terminated path, got %v", got)
	}
}

func TestCWDTrackerSTTerminated(t *testing.T) {
	tr := session.NewCWDTracker()
	got := tr.Feed([]byte("\x1b]9;9;C:\\proj\x1b\\PS> "))
	if len(got) != 1 || got[0] != `C:\proj` {
		t.Fatalf("expected ST-terminated path, got %v", got)
	}
}

func TestCWDTrackerSplitAcrossChunks(t *testing.T) {
	tr := session.NewCWDTracker()
	a := tr.Feed([]byte("\x1b]9;9;C:\\hom"))
	if len(a) != 0 {
		t.Fatalf("partial chunk should not yield a path yet, got %v", a)
	}
	b := tr.Feed([]byte("e\\bob\x07"))
	if len(b) != 1 || b[0] != `C:\home\bob` {
		t.Fatalf("expected reassembled path, got %v", b)
	}
}

func TestCWDTrackerQuotedPath(t *testing.T) {
	tr := session.NewCWDTracker()
	got := tr.Feed([]byte("\x1b]9;9;\"C:\\Program Files\"\x07"))
	if len(got) != 1 || got[0] != `C:\Program Files` {
		t.Fatalf("expected dequoted path, got %v", got)
	}
}

func TestCWDTrackerIgnoresUnrelatedEscapes(t *testing.T) {
	tr := session.NewCWDTracker()
	got := tr.Feed([]byte("plain text \x1b[31mred\x1b[0m text"))
	if len(got) != 0 {
		t.Fatalf("non-OSC-99 escapes should not yield paths, got %v", got)
	}
}

func TestCWDTrackerCapsRunawayBody(t *testing.T) {
	tr := session.NewCWDTracker()
	// A 100 KB OSC body without a terminator must not blow up memory
	// and must not yield a path. The cap (4 KiB) is the load-bearing
	// invariant; downstream recovery is best-effort.
	long := make([]byte, 100<<10)
	for i := range long {
		long[i] = 'x'
	}
	feed := append([]byte("\x1b]9;9;"), long...)
	if got := tr.Feed(feed); len(got) != 0 {
		t.Fatalf("unterminated OSC must not yield paths, got %v", got)
	}
	// Properly terminating the runaway body finishes the parse without
	// emitting a path (the body never matched 9;9;<path>).
	if got := tr.Feed([]byte{0x07}); len(got) != 0 {
		t.Fatalf("BEL on capped runaway body should not yield a path, got %v", got)
	}
	// Now a fresh, well-formed OSC parses correctly.
	got := tr.Feed([]byte("\x1b]9;9;C:\\\x07"))
	if len(got) != 1 || got[0] != `C:\` {
		t.Fatalf("expected fresh OSC after recovery, got %v", got)
	}
}
