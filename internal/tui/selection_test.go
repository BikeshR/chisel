package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestNormalizeSelectionForwardUnchanged(t *testing.T) {
	sl, sc, el, ec := normalizeSelection(1, 2, 3, 4)
	if sl != 1 || sc != 2 || el != 3 || ec != 4 {
		t.Errorf("got (%d,%d,%d,%d), want unchanged (1,2,3,4)", sl, sc, el, ec)
	}
}

func TestNormalizeSelectionBackwardAcrossLinesSwaps(t *testing.T) {
	sl, sc, el, ec := normalizeSelection(5, 2, 1, 8)
	if sl != 1 || sc != 8 || el != 5 || ec != 2 {
		t.Errorf("got (%d,%d,%d,%d), want swapped (1,8,5,2)", sl, sc, el, ec)
	}
}

func TestNormalizeSelectionBackwardSameLineSwaps(t *testing.T) {
	sl, sc, el, ec := normalizeSelection(3, 10, 3, 4)
	if sl != 3 || sc != 4 || el != 3 || ec != 10 {
		t.Errorf("got (%d,%d,%d,%d), want swapped columns (3,4,3,10)", sl, sc, el, ec)
	}
}

func TestSelectionLineRangeClampsToContent(t *testing.T) {
	start, end, ok := selectionLineRange(5, -2, 100)
	if !ok || start != 0 || end != 4 {
		t.Errorf("got (%d,%d,%v), want clamped (0,4,true)", start, end, ok)
	}
}

func TestSelectionLineRangeEmptyWhenEntirelyOutOfBounds(t *testing.T) {
	_, _, ok := selectionLineRange(5, 10, 20)
	if ok {
		t.Error("expected ok=false for a range entirely past the content")
	}
}

func TestSelectionPlainTextSingleLine(t *testing.T) {
	content := "hello world"
	got := selectionPlainText(content, 0, 0, 0, 5)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestSelectionPlainTextMultiLine(t *testing.T) {
	content := "first line\nsecond line\nthird line"
	got := selectionPlainText(content, 0, 6, 2, 5)
	want := "line\nsecond line\nthird"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSelectionPlainTextBackwardDragNormalizes(t *testing.T) {
	content := "first line\nsecond line"
	// Dragged from (1,5) up to (0,0) — end-before-start in raw args.
	got := selectionPlainText(content, 1, 5, 0, 0)
	want := "first line\nsecon"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSelectionPlainTextStripsANSI(t *testing.T) {
	content := "\x1b[38;5;203mRED\x1b[0m plain \x1b[38;5;40mGREEN\x1b[0m"
	got := selectionPlainText(content, 0, 0, 0, ansi.StringWidth(content))
	if strings.Contains(got, "\x1b") {
		t.Errorf("expected copied text to have no ANSI codes, got %q", got)
	}
	if got != "RED plain GREEN" {
		t.Errorf("got %q, want %q", got, "RED plain GREEN")
	}
}

func TestApplySelectionHighlightAddsReverseVideoAndPreservesText(t *testing.T) {
	content := "hello world"
	got := applySelectionHighlight(content, 0, 0, 0, 5)
	if !strings.Contains(got, selReverseOn) || !strings.Contains(got, selReverseOff) {
		t.Errorf("expected reverse-video codes bracketing the selection, got %q", got)
	}
	if ansi.Strip(got) != content {
		t.Errorf("highlighting changed the visible text: got %q, want %q", ansi.Strip(got), content)
	}
}

func TestApplySelectionHighlightPreservesExistingColor(t *testing.T) {
	content := "\x1b[38;5;203mRED TEXT HERE\x1b[0m"
	got := applySelectionHighlight(content, 0, 4, 0, 8)
	if ansi.Strip(got) != ansi.Strip(content) {
		t.Errorf("highlighting changed visible text: got %q, want %q", ansi.Strip(got), ansi.Strip(content))
	}
	if !strings.Contains(got, selReverseOn) {
		t.Error("expected reverse-video to be applied")
	}
}

func TestApplySelectionHighlightMultiLineHighlightsFullMiddleLines(t *testing.T) {
	content := "aaa\nbbb\nccc"
	got := applySelectionHighlight(content, 0, 1, 2, 2)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	// Middle line ("bbb") should be fully wrapped in reverse video.
	if !strings.HasPrefix(lines[1], selReverseOn) {
		t.Errorf("expected the full middle line highlighted, got %q", lines[1])
	}
}

func TestApplySelectionHighlightNoOpForZeroWidthSelection(t *testing.T) {
	content := "hello"
	got := applySelectionHighlight(content, 0, 2, 0, 2)
	if got != content {
		t.Errorf("expected a zero-width selection to leave content unchanged, got %q, want %q", got, content)
	}
}

func TestLineColAtWithinViewportBounds(t *testing.T) {
	m := Model{viewport: viewport.New(80, 10)}
	m.viewport.YOffset = 3
	line, col, ok := m.lineColAt(5, 2)
	if !ok || line != 5 || col != 5 {
		t.Errorf("got (%d,%d,%v), want (5,5,true)", line, col, ok)
	}
}

func TestLineColAtOutsideViewportBounds(t *testing.T) {
	m := Model{viewport: viewport.New(80, 10)}
	if _, _, ok := m.lineColAt(5, 10); ok {
		t.Error("expected y at/past viewport.Height to be out of bounds")
	}
	if _, _, ok := m.lineColAt(-1, 2); ok {
		t.Error("expected negative x to be out of bounds")
	}
}

// newSelectableModel builds a Model with a small viewport and several
// distinct, plain-text lines — enough for mouse-drag selection tests
// without needing to reason about word-wrap boundaries.
func newSelectableModel(t *testing.T) Model {
	t.Helper()
	m := Model{width: 80}
	m.viewport = viewport.New(80, 5)
	m.textArea = textarea.New()
	for _, line := range []string{"alpha beta", "gamma delta", "epsilon zeta"} {
		m.appendLine(line)
	}
	m.viewport.GotoTop()
	return m
}

func TestMouseDragSelectsAndCopiesOnRelease(t *testing.T) {
	m := newSelectableModel(t)

	got, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 0})
	gm := got.(Model)
	if !gm.selecting {
		t.Fatal("expected a left-button press over the viewport to start a selection")
	}

	got, _ = gm.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 5, Y: 0})
	gm = got.(Model)

	got, cmd := gm.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 5, Y: 0})
	gm = got.(Model)
	if gm.selecting {
		t.Error("expected release to end the selection")
	}
	if cmd == nil {
		t.Fatal("expected a Cmd to clear the one-shot clipboard OSC after release")
	}
	if gm.pendingClipboardOSC == "" {
		t.Fatal("expected a pending clipboard OSC sequence to be queued after a non-empty selection")
	}
	wantOSC := ansi.SetSystemClipboard("alpha")
	if gm.pendingClipboardOSC != wantOSC {
		t.Errorf("pendingClipboardOSC = %q, want %q (base64 of the selected text \"alpha\")", gm.pendingClipboardOSC, wantOSC)
	}
}

func TestClearClipboardOSCMsgClearsPendingOSC(t *testing.T) {
	m := Model{pendingClipboardOSC: "\x1b]52;c;deadbeef\x07"}
	got, _ := m.Update(clearClipboardOSCMsg{})
	gm := got.(Model)
	if gm.pendingClipboardOSC != "" {
		t.Errorf("expected pendingClipboardOSC cleared, got %q", gm.pendingClipboardOSC)
	}
}

func TestMouseWheelStillScrollsAlongsideSelectionHandling(t *testing.T) {
	m := bigTranscript(t, 50)
	if !m.viewport.AtBottom() {
		t.Fatal("expected a freshly built transcript to start at the bottom")
	}

	got, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	gotModel := got.(Model)
	if gotModel.viewport.AtBottom() {
		t.Error("expected wheel-up to still scroll the viewport through handleMouseMsg")
	}
	if gotModel.selecting {
		t.Error("a wheel event must not start a text selection")
	}
}

func TestViewEmbedsPendingClipboardOSC(t *testing.T) {
	m := newInputModel()
	m.viewport = viewport.New(80, 5)
	m.pendingClipboardOSC = "\x1b]52;c;deadbeef\x07"
	if !strings.Contains(m.View(), m.pendingClipboardOSC) {
		t.Error("expected View() to embed the pending clipboard OSC sequence")
	}
}
