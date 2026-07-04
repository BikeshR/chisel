package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/skill"
)

func TestSetSkillsAugmentsSystemPromptAndAddsTool(t *testing.T) {
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
	c.SetSkills(map[string]skill.Skill{
		"go-review": {Name: "go-review", Description: "reviews Go code for common bugs"},
	})

	ch, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	for range ch {
	}

	if !strings.Contains(gotBody.Messages[0].Content, "go-review") || !strings.Contains(gotBody.Messages[0].Content, "reviews Go code for common bugs") {
		t.Errorf("system prompt = %q, want it to list the skill's name and description", gotBody.Messages[0].Content)
	}

	found := false
	for _, tool := range gotBody.Tools {
		if tool.Function.Name == "load_skill" {
			found = true
		}
	}
	if !found {
		t.Error("expected load_skill to be added to the tool set once a skill is loaded")
	}
}

func TestSetSkillsEmptyIsNoop(t *testing.T) {
	c := New("minimax-m3")
	before := len(c.tools)
	c.SetSkills(nil)
	if len(c.tools) != before {
		t.Errorf("tool count changed from %d to %d, want no-op for an empty skills map", before, len(c.tools))
	}
	if c.skillsPrompt != "" {
		t.Errorf("skillsPrompt = %q, want empty", c.skillsPrompt)
	}
}

func TestRunLoadSkillFound(t *testing.T) {
	skills := map[string]skill.Skill{
		"go-review": {Name: "go-review", Content: "Check for unchecked errors and race conditions."},
	}
	got, err := runLoadSkill(skills, json.RawMessage(`{"name":"go-review"}`))
	if err != nil {
		t.Fatalf("runLoadSkill: %v", err)
	}
	if got != "Check for unchecked errors and race conditions." {
		t.Errorf("got %q", got)
	}
}

func TestRunLoadSkillNotFound(t *testing.T) {
	_, err := runLoadSkill(map[string]skill.Skill{}, json.RawMessage(`{"name":"nonexistent"}`))
	if err == nil {
		t.Error("expected an error for an unknown skill name")
	}
}

func TestRunLoadSkillMalformedInput(t *testing.T) {
	_, err := runLoadSkill(map[string]skill.Skill{}, json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected an error for malformed input")
	}
}

func TestLoadSkillNeedsNoPermission(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "load_skill", Arguments: `{"name":"x"}`}}
	if NeedsPermission(call) {
		t.Error("load_skill needs permission, want auto-allowed — it only reads a local file the user placed there")
	}
}

func TestSummarizeLoadSkill(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "load_skill", Arguments: `{"name":"go-review"}`}}
	got := Summarize(call)
	if got != "load skill: go-review" {
		t.Errorf("Summarize() = %q", got)
	}
}
