package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetMemoryAugmentsSystemPrompt(t *testing.T) {
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
	c.SetMemory("this repo uses gofmt and tabs")

	ch, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	for range ch {
	}

	if !strings.Contains(gotBody.Messages[0].Content, "this repo uses gofmt and tabs") {
		t.Errorf("system prompt = %q, want it to include the memory content", gotBody.Messages[0].Content)
	}
}
