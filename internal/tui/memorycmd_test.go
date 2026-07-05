package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/agentmemory"
)

func TestHandleMemoryCommandReportsNothingRememberedYet(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	got := m.handleMemoryCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "nothing remembered") {
		t.Errorf("lines = %+v, want a line reporting nothing remembered", lines)
	}
}

func TestHandleMemoryCommandShowsRememberedContent(t *testing.T) {
	dir := t.TempDir()
	if err := agentmemory.Remember(dir, "this repo uses tabs not spaces"); err != nil {
		t.Fatal(err)
	}

	m := Model{workDir: dir}
	got := m.handleMemoryCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "this repo uses tabs not spaces") {
		t.Errorf("lines = %+v, want the remembered note shown", lines)
	}
}

func TestHandleMemoryCommandClearRemovesContent(t *testing.T) {
	dir := t.TempDir()
	if err := agentmemory.Remember(dir, "something to forget"); err != nil {
		t.Fatal(err)
	}
	client := agent.New("minimax-m3")
	client.SetAgentMemory("something to forget")

	m := Model{workDir: dir, client: client}
	got := m.handleMemoryCommand([]string{"clear"})

	if _, found := agentmemory.Load(dir); found {
		t.Error("agentmemory.Load found content after /memory clear, want it removed")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "cleared") {
		t.Errorf("lines = %+v, want a line confirming memory was cleared", lines)
	}
}

func TestHandleMemoryCommandUnknownArgShowsUsage(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	got := m.handleMemoryCommand([]string{"bogus"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v, want a usage line for an unrecognized argument", lines)
	}
}
