package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlanModeToggle(t *testing.T) {
	c := New("minimax-m3")
	if c.PlanMode() {
		t.Error("PlanMode() = true by default, want false")
	}

	c.SetPlanMode(true)
	if !c.PlanMode() {
		t.Error("PlanMode() = false after SetPlanMode(true)")
	}

	c.SetPlanMode(false)
	if c.PlanMode() {
		t.Error("PlanMode() = true after SetPlanMode(false)")
	}
}

// TestModeDefaultsToNormal confirms a fresh Client starts in the plain,
// everything-asks mode — neither plan mode nor accept-edits.
func TestModeDefaultsToNormal(t *testing.T) {
	c := New("minimax-m3")
	if c.Mode() != ModeNormal {
		t.Errorf("Mode() = %v, want ModeNormal by default", c.Mode())
	}
}

// TestSetModeThreeWay exercises the full three-way enum directly,
// independent of the PlanMode/SetPlanMode boolean convenience wrappers.
func TestSetModeThreeWay(t *testing.T) {
	c := New("minimax-m3")

	c.SetMode(ModeAcceptEdits)
	if c.Mode() != ModeAcceptEdits {
		t.Errorf("Mode() = %v, want ModeAcceptEdits", c.Mode())
	}
	if c.PlanMode() {
		t.Error("PlanMode() = true while in ModeAcceptEdits, want false")
	}

	c.SetMode(ModePlan)
	if c.Mode() != ModePlan {
		t.Errorf("Mode() = %v, want ModePlan", c.Mode())
	}
	if !c.PlanMode() {
		t.Error("PlanMode() = false while in ModePlan, want true")
	}

	c.SetMode(ModeNormal)
	if c.Mode() != ModeNormal {
		t.Errorf("Mode() = %v, want ModeNormal", c.Mode())
	}
}

// TestSetPlanModeFalseDoesNotClobberAcceptEdits is the regression test
// for the exact hazard introducing a third mode created: SetPlanMode
// predates the enum and is kept only as a plan/not-plan convenience —
// calling it with false must not force ModeNormal unconditionally, or
// it would silently turn off accept-edits mode for any caller that
// merely meant "make sure plan mode specifically is off."
func TestSetPlanModeFalseDoesNotClobberAcceptEdits(t *testing.T) {
	c := New("minimax-m3")
	c.SetMode(ModeAcceptEdits)

	c.SetPlanMode(false)

	if c.Mode() != ModeAcceptEdits {
		t.Errorf("Mode() = %v, want ModeAcceptEdits preserved — SetPlanMode(false) must only un-plan, not reset to ModeNormal unconditionally", c.Mode())
	}
}

// TestCloneResetsAcceptEditsToo confirms Clone's existing "always
// ModeNormal" guarantee (previously "always plan mode off") still
// holds for the new mode too — a /model check probe must never
// auto-approve edits just because the live client happened to be in
// accept-edits mode when it was cloned.
func TestCloneResetsAcceptEditsToo(t *testing.T) {
	c := New("minimax-m3")
	c.SetMode(ModeAcceptEdits)

	clone := c.Clone("glm-5.2")

	if clone.Mode() != ModeNormal {
		t.Errorf("clone.Mode() = %v, want ModeNormal", clone.Mode())
	}
}

// TestPlanModeAugmentsSystemPrompt verifies the actual request body sent
// on the wire, not just the Client's own flag — the flag is only useful
// insofar as it changes what the model is told.
func TestPlanModeAugmentsSystemPrompt(t *testing.T) {
	var gotBody chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	c := New("minimax-m3")
	ch, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	for range ch {
	}

	if len(gotBody.Messages) == 0 || gotBody.Messages[0].Role != "system" {
		t.Fatalf("gotBody.Messages = %+v, want a leading system message", gotBody.Messages)
	}
	if strings.Contains(gotBody.Messages[0].Content, "PLAN MODE") {
		t.Error("system prompt mentions PLAN MODE with plan mode off")
	}

	c.SetPlanMode(true)
	ch, err = c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming (plan mode): %v", err)
	}
	for range ch {
	}

	if !strings.Contains(gotBody.Messages[0].Content, "PLAN MODE") {
		t.Errorf("system prompt = %q, want it to mention PLAN MODE once enabled", gotBody.Messages[0].Content)
	}
}
