package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandleModelPlannerCommandSetShowAndClear(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}

	got := m.handleModelPlannerCommand([]string{"glm-5.2"})
	if got.client.PlannerModel() != "glm-5.2" {
		t.Errorf("PlannerModel() = %q, want it set", got.client.PlannerModel())
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "glm-5.2") {
		t.Errorf("lines = %+v, want a line confirming the planner model was set", lines)
	}

	got = got.handleModelPlannerCommand(nil)
	lines = got.renderedLines()
	if len(lines) != 2 || !strings.Contains(lines[1], "glm-5.2") {
		t.Errorf("lines = %+v, want a bare /model planner to show the current one", lines)
	}

	got = got.handleModelPlannerCommand([]string{"clear"})
	if got.client.PlannerModel() != "" {
		t.Errorf("PlannerModel() = %q, want cleared", got.client.PlannerModel())
	}
	lines = got.renderedLines()
	if len(lines) != 3 || !strings.Contains(lines[2], "cleared") {
		t.Errorf("lines = %+v, want a line confirming the planner model was cleared", lines)
	}
}

func TestHandleModelPlannerCommandBareWithNoneSet(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got := m.handleModelPlannerCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no planner model set") {
		t.Errorf("lines = %+v, want a line reporting no planner model is set", lines)
	}
}

// TestModelCommandRoutesPlannerSubcommand confirms /model planner is
// actually reachable through the normal /model dispatch, not just
// handleModelPlannerCommand in isolation.
func TestModelCommandRoutesPlannerSubcommand(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, cmd := m.handleModelCommand([]string{"planner", "glm-5.2"})
	if cmd != nil {
		t.Error("expected a nil Cmd — setting the planner model is synchronous")
	}
	if got.client.PlannerModel() != "glm-5.2" {
		t.Errorf("PlannerModel() = %q, want it set via /model planner", got.client.PlannerModel())
	}
}
