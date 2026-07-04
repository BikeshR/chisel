package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestDrainSuccess(t *testing.T) {
	ch := make(chan Event, 2)
	ch <- Event{TextDelta: "hi"}
	ch <- Event{Done: true, Message: &Message{Role: "assistant", Content: "hi"}, Usage: Usage{InputTokens: 10}}
	close(ch)

	msg, usage, err := Drain(ch)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if msg.Content != "hi" {
		t.Errorf("msg.Content = %q", msg.Content)
	}
	if usage.InputTokens != 10 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestDrainStreamError(t *testing.T) {
	ch := make(chan Event, 1)
	ch <- Event{Done: true, Err: errors.New("boom")}
	close(ch)

	_, _, err := Drain(ch)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want it to mention \"boom\"", err)
	}
}

// TestDrainChannelClosedWithoutDone is the regression test for the
// nil-deref-if-unreachable risk this function exists to remove: every
// caller used to loop "if ev.Done { final = ev }" and then dereference
// final.Message after checking only final.Err — safe only because
// decodeStream's contract says a Done event always arrives. If that
// contract were ever violated (channel closes with no Done event at
// all), the old pattern would leave final as the zero Event and panic on
// *final.Message. Drain must turn that into a clean error instead.
func TestDrainChannelClosedWithoutDone(t *testing.T) {
	ch := make(chan Event)
	close(ch) // closed immediately, no Done event ever sent

	msg, _, err := Drain(ch)
	if err == nil {
		t.Fatal("expected an error when the channel closes without a Done event")
	}
	if msg != nil {
		t.Errorf("msg = %+v, want nil", msg)
	}
}

// TestDrainDoneWithNilMessage covers the other half of the same
// contract: a Done event with Err == nil is supposed to always carry a
// non-nil Message. Drain must not just trust that either.
func TestDrainDoneWithNilMessage(t *testing.T) {
	ch := make(chan Event, 1)
	ch <- Event{Done: true} // Done, no Err, but Message is nil
	close(ch)

	msg, _, err := Drain(ch)
	if err == nil {
		t.Fatal("expected an error for a Done event with a nil Message")
	}
	if msg != nil {
		t.Errorf("msg = %+v, want nil", msg)
	}
}
