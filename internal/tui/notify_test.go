package tui

import (
	"strings"
	"testing"
	"time"
)

func TestOSC9NotifyFormat(t *testing.T) {
	got := osc9Notify("hello")
	if !strings.HasPrefix(got, "\x1b]9;") || !strings.HasSuffix(got, "\x07") || !strings.Contains(got, "hello") {
		t.Errorf("got %q, want a well-formed OSC 9 sequence wrapping the message", got)
	}
}

// TestNotifyIdleProducesMsgNotDirectSideEffect is the regression test
// for a real race: notifyIdle used to write the bell+OSC-9 sequence
// straight to os.Stdout from its own Cmd goroutine, concurrently with
// bubbletea's renderer — the exact hazard the OSC-52 clipboard code
// documents avoiding. It must instead produce a notifyIdleMsg for
// Update to turn into a queued, render-cycle-safe escape sequence.
func TestNotifyIdleProducesMsgNotDirectSideEffect(t *testing.T) {
	cmd := notifyIdle("test")
	if cmd == nil {
		t.Fatal("notifyIdle returned a nil Cmd")
	}
	msg, ok := cmd().(notifyIdleMsg)
	if !ok {
		t.Fatalf("notifyIdle's Cmd produced %T, want notifyIdleMsg", cmd())
	}
	if msg.message != "test" {
		t.Errorf("message = %q, want %q", msg.message, "test")
	}
}

// TestUpdateQueuesNotifyOSCThenClearsIt confirms the full round trip:
// notifyIdleMsg queues the escape sequence in Model.pendingNotifyOSC
// (rendered by View, the same one-shot mechanism the OSC-52 clipboard
// sequence already uses), and clearNotifyOSCMsg removes it after one
// render cycle.
func TestUpdateQueuesNotifyOSCThenClearsIt(t *testing.T) {
	oldDelay := clearNotifyOSCDelay
	clearNotifyOSCDelay = time.Millisecond
	defer func() { clearNotifyOSCDelay = oldDelay }()

	m := Model{}

	got, cmd := m.Update(notifyIdleMsg{message: "chisel is done"})
	gotModel := got.(Model)
	if gotModel.pendingNotifyOSC == "" {
		t.Fatal("expected pendingNotifyOSC to be queued")
	}
	if !strings.Contains(gotModel.pendingNotifyOSC, "chisel is done") {
		t.Errorf("pendingNotifyOSC = %q, want it to contain the message", gotModel.pendingNotifyOSC)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to clear it after one render cycle")
	}

	cleared, _ := gotModel.Update(cmd())
	if cleared.(Model).pendingNotifyOSC != "" {
		t.Error("expected pendingNotifyOSC cleared after clearNotifyOSCMsg")
	}
}

// TestClearNotifyOSCCmdIsDelayedNotImmediate is the regression test for
// a real dropped-notification bug: firing the clear immediately (an
// instant Cmd, dispatched right alongside the one that sets
// pendingNotifyOSC) risked both landing within the same render tick —
// processed back to back with no frame painted in between, silently
// dropping the bell/OSC-9 notification. The clear must actually take
// clearNotifyOSCDelay to fire, not resolve instantly.
func TestClearNotifyOSCCmdIsDelayedNotImmediate(t *testing.T) {
	oldDelay := clearNotifyOSCDelay
	clearNotifyOSCDelay = 50 * time.Millisecond
	defer func() { clearNotifyOSCDelay = oldDelay }()

	start := time.Now()
	msg := clearNotifyOSCCmd()()
	elapsed := time.Since(start)

	if _, ok := msg.(clearNotifyOSCMsg); !ok {
		t.Fatalf("got %T, want clearNotifyOSCMsg", msg)
	}
	if elapsed < clearNotifyOSCDelay {
		t.Errorf("clearNotifyOSCCmd resolved in %s, want it to take at least %s", elapsed, clearNotifyOSCDelay)
	}
}
