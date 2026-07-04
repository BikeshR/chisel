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
