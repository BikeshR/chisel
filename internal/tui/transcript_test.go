package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWrapToWidthPreservesANSIAndWrapsVisibleText(t *testing.T) {
	styled := "\x1b[1;38;2;100;150;255myou  \x1b[0m" + strings.Repeat("word ", 20)
	wrapped := wrapToWidth(styled, 20)

	if !strings.Contains(wrapped, "\x1b[1;38;2;100;150;255m") {
		t.Error("wrapping lost the embedded ANSI style code")
	}
	for i, line := range strings.Split(wrapped, "\n") {
		if w := lipgloss.Width(line); w > 20 {
			t.Errorf("line %d visible width = %d, want <= 20 (line: %q)", i, w, line)
		}
	}
}

func TestWrapToWidthPassesThroughWhenWidthUnknown(t *testing.T) {
	s := "some unwrapped text"
	if got := wrapToWidth(s, 0); got != s {
		t.Errorf("wrapToWidth with width 0 = %q, want unchanged %q", got, s)
	}
	if got := wrapToWidth(s, -1); got != s {
		t.Errorf("wrapToWidth with negative width = %q, want unchanged %q", got, s)
	}
}

func TestTranscriptContentRewrapsAtDifferentWidths(t *testing.T) {
	m := Model{}
	m.appendLine(strings.Repeat("word ", 30))

	m.width = 20
	narrow := strings.Count(m.transcriptContent(), "\n")

	m.width = 200
	wide := strings.Count(m.transcriptContent(), "\n")

	if narrow <= wide {
		t.Errorf("expected re-wrapping at a narrower width to produce more lines (narrow=%d, wide=%d)", narrow, wide)
	}
}

func TestThinkToggleReRendersHistoricalEntries(t *testing.T) {
	m := Model{showThinking: false}
	m.entries = append(m.entries, entry{isAssistant: true, raw: "before <think>hidden reasoning</think> after"})

	collapsed := m.renderedLines()[0]
	if strings.Contains(collapsed, "hidden reasoning") {
		t.Fatalf("collapsed render shows raw think content: %q", collapsed)
	}

	m.showThinking = true
	expanded := m.renderedLines()[0]
	if !strings.Contains(expanded, "hidden reasoning") {
		t.Errorf("expanded render (same entry, toggled showThinking) = %q, want it to include the think content", expanded)
	}
}

func TestAppendAssistantEntrySupportsThinkToggle(t *testing.T) {
	m := Model{}
	m.appendAssistantEntry("visible <think>secret</think> text")

	if strings.Contains(m.renderedLines()[0], "secret") {
		t.Error("appendAssistantEntry's line shows think content by default")
	}

	m.showThinking = true
	if !strings.Contains(m.renderedLines()[0], "secret") {
		t.Error("toggling showThinking after appendAssistantEntry should reveal the think content")
	}
}

func TestTranscriptContentRendersMarkdownForCompletedAssistantEntry(t *testing.T) {
	m := Model{width: 80}
	m.appendAssistantEntry("Some **bold** text.")

	content := m.transcriptContent()
	if strings.Contains(content, "**bold**") {
		t.Errorf("expected markdown to be rendered rather than left as literal syntax, got: %q", content)
	}
	if !strings.Contains(content, "chisel") {
		t.Errorf("expected the chisel prefix to survive markdown rendering, got: %q", content)
	}
}

func TestTranscriptContentSkipsMarkdownWhileStreaming(t *testing.T) {
	m := Model{width: 80, streamLineIdx: -1}
	m.appendStreamText("Some **bold** text")

	content := m.transcriptContent()
	if !strings.Contains(content, "**bold**") {
		t.Errorf("expected literal markdown syntax while still streaming, got: %q", content)
	}
}

func TestTranscriptContentSkipsMarkdownWhenThinkTagsPresent(t *testing.T) {
	m := Model{width: 80}
	m.appendAssistantEntry("<think>hidden</think>Some **bold** text.")

	content := m.transcriptContent()
	if !strings.Contains(content, "**bold**") {
		t.Errorf("expected renderAssistantText's plain-text path (not markdown) when raw contains think tags, got: %q", content)
	}
}

func TestTranscriptContentCacheReflectsToolResultExpandToggle(t *testing.T) {
	m := Model{width: 80}
	m.appendToolResultEntry("line one\nline two\nline three", false)

	collapsed := m.transcriptContent()
	if strings.Contains(collapsed, "line two") {
		t.Errorf("collapsed tool result should show only the first line, got: %q", collapsed)
	}

	m.toggleLastToolResult()
	expanded := m.transcriptContent()
	if !strings.Contains(expanded, "line two") {
		t.Errorf("expanded tool result (post cache-invalidation) should show every line, got: %q", expanded)
	}
}
