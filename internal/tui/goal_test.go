package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandleGoalCommandSetShowAndClear(t *testing.T) {
	m := Model{}

	got := m.handleGoalCommand("ship the login feature")
	if got.goal != "ship the login feature" {
		t.Errorf("goal = %q, want it set", got.goal)
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "ship the login feature") {
		t.Errorf("lines = %+v, want a line confirming the goal was set", lines)
	}

	got = got.handleGoalCommand("")
	lines = got.renderedLines()
	if len(lines) != 2 || !strings.Contains(lines[1], "ship the login feature") {
		t.Errorf("lines = %+v, want a bare /goal to show the current one", lines)
	}

	got = got.handleGoalCommand("clear")
	if got.goal != "" {
		t.Errorf("goal = %q, want cleared", got.goal)
	}
	lines = got.renderedLines()
	if len(lines) != 3 || !strings.Contains(lines[2], "cleared") {
		t.Errorf("lines = %+v, want a line confirming the goal was cleared", lines)
	}
}

func TestHandleGoalCommandBareWithNoGoalSet(t *testing.T) {
	m := Model{}
	got := m.handleGoalCommand("")
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no goal set") {
		t.Errorf("lines = %+v, want a line reporting no goal is set", lines)
	}
}

// TestHandleStreamCompleteAutoContinuesTowardGoal is the direct
// regression test for the whole feature: a turn ending with no more
// tool calls and a standing goal should auto-submit a continuation
// instead of going idle.
func TestHandleStreamCompleteAutoContinuesTowardGoal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"still working\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	m := Model{client: agent.New("minimax-m3"), goal: "refactor the auth package"}
	got, cmd := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done with step one"},
		agent.Usage{},
		"stop",
	)
	gotModel := got.(Model)

	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel — a goal continuation should start a new turn", gotModel.state)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the continuation")
	}
	if len(gotModel.messages) != 2 || !strings.Contains(gotModel.messages[1].Content, "refactor the auth package") {
		t.Errorf("messages = %+v, want a continuation message mentioning the goal", gotModel.messages)
	}
	if gotModel.goalContinuations != 1 {
		t.Errorf("goalContinuations = %d, want 1", gotModel.goalContinuations)
	}
}

func TestHandleStreamCompleteNoGoalContinuationWithoutAGoal(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, _ := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done"},
		agent.Usage{},
		"stop",
	)
	gotModel := got.(Model)

	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput — no goal set, should go idle normally", gotModel.state)
	}
}

// TestHandleStreamCompleteQueuedMessageWinsOverGoal confirms a queued
// message (the user actively steering) takes priority over auto-
// continuing toward a standing goal — the same priority queued
// messages already have over auto-compact.
func TestHandleStreamCompleteQueuedMessageWinsOverGoal(t *testing.T) {
	m := Model{
		client:         agent.New("minimax-m3"),
		goal:           "refactor the auth package",
		queuedMessages: []string{"actually, do this instead"},
	}
	got, _ := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done"},
		agent.Usage{},
		"stop",
	)
	gotModel := got.(Model)

	if len(gotModel.messages) != 2 || gotModel.messages[1].Content != "actually, do this instead" {
		t.Errorf("messages = %+v, want the queued message delivered instead of a goal continuation", gotModel.messages)
	}
	if gotModel.goalContinuations != 0 {
		t.Errorf("goalContinuations = %d, want 0 — the goal continuation path must not have run", gotModel.goalContinuations)
	}
}

// TestContinueTowardGoalStopsAtLimit is the safety-critical test for
// maxGoalContinuations: it must not auto-continue forever.
func TestContinueTowardGoalStopsAtLimit(t *testing.T) {
	m := Model{goal: "endless task", goalContinuations: maxGoalContinuations}
	got, cmd := m.continueTowardGoal()

	if cmd != nil {
		t.Error("expected a nil Cmd once the continuation limit is reached")
	}
	if got.goal != "" {
		t.Errorf("goal = %q, want cleared once the limit is reached", got.goal)
	}
	lines := got.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "limit reached") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line explaining the limit was reached", lines)
	}
}

// TestHandleStreamCompleteTracksRepeatedAssistantText confirms the
// counter itself increments on identical consecutive responses and
// resets once the text actually changes.
func TestHandleStreamCompleteTracksRepeatedAssistantText(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}

	got, _ := m.handleStreamComplete(agent.Message{Role: "assistant", Content: "still working on it"}, agent.Usage{}, "stop")
	gotModel := got.(Model)
	if gotModel.assistantTextRepeatCount != 1 {
		t.Errorf("assistantTextRepeatCount = %d, want 1 after the first response", gotModel.assistantTextRepeatCount)
	}

	got, _ = gotModel.handleStreamComplete(agent.Message{Role: "assistant", Content: "still working on it"}, agent.Usage{}, "stop")
	gotModel = got.(Model)
	if gotModel.assistantTextRepeatCount != 2 {
		t.Errorf("assistantTextRepeatCount = %d, want 2 after an identical repeat", gotModel.assistantTextRepeatCount)
	}

	got, _ = gotModel.handleStreamComplete(agent.Message{Role: "assistant", Content: "actually, found the bug"}, agent.Usage{}, "stop")
	gotModel = got.(Model)
	if gotModel.assistantTextRepeatCount != 1 {
		t.Errorf("assistantTextRepeatCount = %d, want reset to 1 once the text changes", gotModel.assistantTextRepeatCount)
	}
}

// TestContinueTowardGoalStopsOnRepeatedAssistantText is the direct
// regression test for the whole feature: a goal auto-continuation must
// not keep firing once the model's own responses are stuck repeating
// verbatim — that would just burn through maxGoalContinuations for no
// actual progress.
func TestContinueTowardGoalStopsOnRepeatedAssistantText(t *testing.T) {
	m := Model{goal: "fix the flaky test", assistantTextRepeatCount: assistantTextRepeatThreshold}
	got, cmd := m.continueTowardGoal()

	if cmd != nil {
		t.Error("expected a nil Cmd once repeated text hits the threshold")
	}
	if got.goal != "" {
		t.Errorf("goal = %q, want cleared once stuck", got.goal)
	}
	lines := got.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "identical") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line explaining the repeated-text stop", lines)
	}
}

// TestContinueTowardGoalProceedsBelowRepeatThreshold confirms the guard
// doesn't trip prematurely — a couple of turns happening to share text
// isn't yet "stuck."
func TestContinueTowardGoalProceedsBelowRepeatThreshold(t *testing.T) {
	m := newInputModel()
	m.goal = "fix the flaky test"
	m.assistantTextRepeatCount = assistantTextRepeatThreshold - 1

	_, cmd := m.continueTowardGoal()
	if cmd == nil {
		t.Error("expected a non-nil Cmd — the repeat threshold hasn't been hit yet")
	}
}

// TestSubmitResetsAssistantTextRepeatCount mirrors
// TestSubmitResetsGoalContinuations for the new counter — a real,
// keystroke-driven submission means the user is actively steering, so
// a fresh run should get the full repeat-detection budget again.
func TestSubmitResetsAssistantTextRepeatCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	m := newInputModel()
	m.client = agent.New("minimax-m3")
	m.assistantTextRepeatCount = 2
	m.textArea.SetValue("hello")

	got, _ := m.submit()
	gotModel := got.(Model)

	if gotModel.assistantTextRepeatCount != 0 {
		t.Errorf("assistantTextRepeatCount = %d, want reset to 0 by a real submission", gotModel.assistantTextRepeatCount)
	}
}

// TestSubmitResetsGoalContinuations confirms a real, keystroke-driven
// submission resets the continuation count — a fresh run of
// continuations should get the full budget again rather than
// inheriting a partially-spent one from before the user typed anything.
func TestSubmitResetsGoalContinuations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	m := newInputModel()
	m.client = agent.New("minimax-m3")
	m.goalContinuations = 10
	m.textArea.SetValue("hello")

	got, _ := m.submit()
	gotModel := got.(Model)

	if gotModel.goalContinuations != 0 {
		t.Errorf("goalContinuations = %d, want reset to 0 by a real submission", gotModel.goalContinuations)
	}
}
