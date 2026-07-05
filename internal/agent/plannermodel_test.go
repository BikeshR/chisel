package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlannerModelDefaultsToEmpty(t *testing.T) {
	c := New("minimax-m3")
	if c.PlannerModel() != "" {
		t.Errorf("PlannerModel() = %q, want empty by default", c.PlannerModel())
	}
}

func TestEffectiveModelNameFallsBackWithoutPlannerModel(t *testing.T) {
	c := New("minimax-m3")
	c.SetMode(ModePlan)

	if got := c.EffectiveModelName(); got != "minimax-m3" {
		t.Errorf("EffectiveModelName() = %q, want the primary model when no planner model is set", got)
	}
}

func TestEffectiveModelNameUsesPlannerModelInPlanMode(t *testing.T) {
	c := New("minimax-m3")
	c.SetPlannerModel("glm-5.2")
	c.SetMode(ModePlan)

	if got := c.EffectiveModelName(); got != "glm-5.2" {
		t.Errorf("EffectiveModelName() = %q, want the planner model in plan mode", got)
	}
}

// TestEffectiveModelNameIgnoresPlannerModelOutsidePlanMode confirms the
// planner model only ever applies to plan-mode turns — accept-edits
// and normal mode both use the primary model regardless.
func TestEffectiveModelNameIgnoresPlannerModelOutsidePlanMode(t *testing.T) {
	c := New("minimax-m3")
	c.SetPlannerModel("glm-5.2")

	if got := c.EffectiveModelName(); got != "minimax-m3" {
		t.Errorf("EffectiveModelName() = %q, want the primary model in normal mode", got)
	}

	c.SetMode(ModeAcceptEdits)
	if got := c.EffectiveModelName(); got != "minimax-m3" {
		t.Errorf("EffectiveModelName() = %q, want the primary model in accept-edits mode too", got)
	}
}

func TestModelNameAlwaysReturnsThePrimaryModel(t *testing.T) {
	c := New("minimax-m3")
	c.SetPlannerModel("glm-5.2")
	c.SetMode(ModePlan)

	if got := c.ModelName(); got != "minimax-m3" {
		t.Errorf("ModelName() = %q, want the primary model unconditionally, unlike EffectiveModelName", got)
	}
}

// TestSendStreamingUsesPlannerModelInPlanMode verifies the actual
// request body sent on the wire, not just EffectiveModelName in
// isolation — the same way TestPlanModeAugmentsSystemPrompt checks the
// real wire effect of plan mode's system-prompt note.
func TestSendStreamingUsesPlannerModelInPlanMode(t *testing.T) {
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
	c.SetPlannerModel("glm-5.2")

	ch, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	for range ch {
	}
	if gotBody.Model != "minimax-m3" {
		t.Errorf("Model = %q, want the primary model outside plan mode", gotBody.Model)
	}

	c.SetMode(ModePlan)
	ch, err = c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming (plan mode): %v", err)
	}
	for range ch {
	}
	if gotBody.Model != "glm-5.2" {
		t.Errorf("Model = %q, want the planner model once in plan mode", gotBody.Model)
	}
}
