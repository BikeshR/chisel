package tui

import (
	"strings"
	"testing"
)

func TestOSC9NotifyFormat(t *testing.T) {
	got := osc9Notify("hello")
	if !strings.HasPrefix(got, "\x1b]9;") || !strings.HasSuffix(got, "\x07") || !strings.Contains(got, "hello") {
		t.Errorf("got %q, want a well-formed OSC 9 sequence wrapping the message", got)
	}
}

func TestNotifyIdleReturnsNilMsg(t *testing.T) {
	cmd := notifyIdle("test")
	if cmd == nil {
		t.Fatal("notifyIdle returned a nil Cmd")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("notifyIdle's Cmd produced %v, want nil (it's a side effect, not a message)", msg)
	}
}
