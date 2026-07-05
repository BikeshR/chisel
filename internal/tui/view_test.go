package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestStatusLine(t *testing.T) {
	m := Model{
		client:            agent.New("minimax-m3"),
		lastContextTokens: 12400,
		tokensIn:          45200,
		tokensOut:         8100,
	}
	line := m.statusLine(200)

	if !strings.Contains(line, "minimax-m3") {
		t.Errorf("status line = %q, want the model name", line)
	}
	if !strings.Contains(line, "12.4k") {
		t.Errorf("status line = %q, want the current context size", line)
	}
	if !strings.Contains(line, "45.2k") || !strings.Contains(line, "8.1k") {
		t.Errorf("status line = %q, want cumulative spend", line)
	}
	if strings.Contains(line, "consider /compact") {
		t.Error("status line suggests /compact below the warning threshold")
	}
}

// TestStatusLineShowsPlannerModelInPlanMode confirms the status bar
// shows whichever model is actually about to run (EffectiveModelName),
// not always the primary one — otherwise a configured planner model
// switching in behind the scenes during plan mode would be invisible.
func TestStatusLineShowsPlannerModelInPlanMode(t *testing.T) {
	client := agent.New("minimax-m3")
	client.SetPlannerModel("glm-5.2")
	m := Model{client: client}

	if !strings.Contains(m.statusLine(200), "minimax-m3") {
		t.Error("status line doesn't show the primary model outside plan mode")
	}

	client.SetMode(agent.ModePlan)
	line := m.statusLine(200)
	if !strings.Contains(line, "glm-5.2") {
		t.Errorf("status line = %q, want the planner model shown once in plan mode", line)
	}
	if strings.Contains(line, "minimax-m3") {
		t.Errorf("status line = %q, want only the effective (planner) model shown, not the primary one too", line)
	}
}

func TestStatusLineWarnsPastThreshold(t *testing.T) {
	m := Model{
		client:            agent.New("minimax-m3"),
		lastContextTokens: contextWarnThreshold + 1,
	}
	line := m.statusLine(200)
	if !strings.Contains(line, "consider /compact") {
		t.Errorf("status line = %q, want a /compact suggestion past the threshold", line)
	}
}

func TestStatusLineShowsGitBranchAndDirtyMarker(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), gitIsRepo: true, gitBranch: "main", gitDirty: true}
	line := m.statusLine(200)
	if !strings.Contains(line, "main*") {
		t.Errorf("status line = %q, want the branch name with a dirty marker", line)
	}
}

func TestStatusLineCleanBranchHasNoDirtyMarker(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), gitIsRepo: true, gitBranch: "main", gitDirty: false}
	line := m.statusLine(200)
	if strings.Contains(line, "main*") {
		t.Errorf("status line = %q, want no dirty marker for a clean tree", line)
	}
	if !strings.Contains(line, "main") {
		t.Errorf("status line = %q, want the branch name shown", line)
	}
}

func TestStatusLineOmitsGitSegmentWhenNotARepo(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), gitIsRepo: false, gitBranch: "main"}
	line := m.statusLine(200)
	if strings.Contains(line, "main") {
		t.Errorf("status line = %q, want no branch shown when gitIsRepo is false", line)
	}
}

func TestStatusLineDropsGitSegmentFirstWhenNarrow(t *testing.T) {
	m := Model{
		client:         agent.New("minimax-m3"),
		gitIsRepo:      true,
		gitBranch:      "a-fairly-long-feature-branch-name",
		gitDirty:       true,
		queuedMessages: []string{"one"},
	}
	wide := m.statusLine(300)
	if !strings.Contains(wide, "a-fairly-long-feature-branch-name") {
		t.Fatalf("wide status line = %q, want the branch shown when there's room", wide)
	}

	narrow := m.statusLine(40)
	if strings.Contains(narrow, "a-fairly-long-feature-branch-name") {
		t.Errorf("narrow status line = %q, want the git segment dropped before other segments", narrow)
	}
}
