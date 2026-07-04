package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// clearClipboardOSCMsg clears the one-shot OSC-52 escape sequence queued
// by a completed selection (see handleMouseMsg/Model.pendingClipboardOSC)
// once it's had a render cycle to actually reach the terminal.
// clearClipboardOSCCmd fires this immediately — computing and queuing the
// sequence itself is synchronous (no I/O, just a base64 encode), so only
// the "clear it after one frame" half needs to round-trip through a Cmd.
type clearClipboardOSCMsg struct{}

func clearClipboardOSCCmd() tea.Cmd {
	return func() tea.Msg { return clearClipboardOSCMsg{} }
}

// handleMouseMsg drives text selection (left-button drag) alongside the
// existing wheel-scroll handling — both read the same event, so wheel
// events keep working exactly as before (viewport.Update ignores
// anything that isn't a wheel press) while left-button press/motion/
// release are handled here for selecting and copying text.
//
// Deliberately doesn't write to the system clipboard directly from a Cmd
// goroutine: OSC-52 is just an escape sequence, but writing raw bytes to
// os.Stdout from anywhere other than bubbletea's own renderer goroutine
// risks interleaving with a frame it's mid-write on. Queuing it in
// Model.pendingClipboardOSC and letting View() embed it (it's zero-width
// and invisible — verified directly: lipgloss.Width/ansi.StringWidth
// both report 0 extra columns for an embedded OSC-52 sequence) goes
// through the same safe path as everything else on screen.
//
// No auto-scroll while dragging past the top/bottom edge of the
// viewport, and the highlight disappears the instant the button is
// released rather than persisting until the next click — both accepted
// as scope cuts: the clipboard copy itself already happens on release,
// so a lingering highlight is confirmation-only, not functional.
func (m Model) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)

	if msg.Button != tea.MouseButtonLeft {
		return m, cmd
	}

	switch msg.Action {
	case tea.MouseActionPress:
		if line, col, ok := m.lineColAt(msg.X, msg.Y); ok {
			m.selecting = true
			m.selStartLine, m.selStartCol = line, col
			m.selEndLine, m.selEndCol = line, col
			m.refreshViewport()
		}

	case tea.MouseActionMotion:
		if m.selecting {
			if line, col, ok := m.lineColAt(msg.X, msg.Y); ok {
				m.selEndLine, m.selEndCol = line, col
				m.refreshViewport()
			}
		}

	case tea.MouseActionRelease:
		if m.selecting {
			m.selecting = false
			text := selectionPlainText(m.transcriptContent(), m.selStartLine, m.selStartCol, m.selEndLine, m.selEndCol)
			m.refreshViewport()
			if text != "" {
				m.pendingClipboardOSC = ansi.SetSystemClipboard(text)
				return m, clearClipboardOSCCmd()
			}
		}
	}

	return m, cmd
}

// lineColAt maps a mouse event's screen coordinates to a (line, col)
// position within the full wrapped transcript content — line indexes
// into transcriptContent()'s lines (not just what's currently visible),
// offset by the viewport's own scroll position. ok is false when y falls
// outside the viewport's rows (over the todo block, input box, or status
// bar instead), none of which support text selection.
func (m Model) lineColAt(x, y int) (line, col int, ok bool) {
	if y < 0 || y >= m.viewport.Height || x < 0 {
		return 0, 0, false
	}
	return m.viewport.YOffset + y, x, true
}

// normalizeSelection reorders a (startLine, startCol) → (endLine, endCol)
// pair so the first is always the earlier position — a drag can go in
// any direction (up, down, or backward on the same line).
func normalizeSelection(sl, sc, el, ec int) (int, int, int, int) {
	if sl > el || (sl == el && sc > ec) {
		return el, ec, sl, sc
	}
	return sl, sc, el, ec
}

// selectionLineRange clamps a normalized selection's line span to
// content's actual line count, reporting ok=false if nothing in range
// survives clamping (an empty transcript, or a selection entirely below
// the last line).
func selectionLineRange(numLines, sl, el int) (start, end int, ok bool) {
	if sl < 0 {
		sl = 0
	}
	if el >= numLines {
		el = numLines - 1
	}
	if sl > el || el < 0 {
		return 0, 0, false
	}
	return sl, el, true
}

// selectionPlainText extracts the ANSI-stripped text spanning a
// selection from content (as produced by transcriptContent) — what
// actually gets copied to the clipboard, since a clipboard full of raw
// escape codes would be useless to paste anywhere else.
func selectionPlainText(content string, startLine, startCol, endLine, endCol int) string {
	lines := strings.Split(content, "\n")
	sl, sc, el, ec := normalizeSelection(startLine, startCol, endLine, endCol)
	sl, el, ok := selectionLineRange(len(lines), sl, el)
	if !ok {
		return ""
	}

	out := make([]string, 0, el-sl+1)
	for i := sl; i <= el; i++ {
		left, right := 0, ansi.StringWidth(lines[i])
		if i == sl {
			left = sc
		}
		if i == el {
			right = ec
		}
		out = append(out, ansi.Strip(ansi.Cut(lines[i], left, right)))
	}
	return strings.Join(out, "\n")
}

// selReverseOn/selReverseOff bracket the currently-dragged selection with
// reverse video (SGR 7) — visible regardless of whatever foreground/
// background the surrounding styled text already has, which is exactly
// what reverse video is for.
const (
	selReverseOn  = "\x1b[7m"
	selReverseOff = "\x1b[27m"
)

// applySelectionHighlight re-renders content with the given selection
// shown in reverse video. Uses ansi.Cut to split each affected line at
// the selection boundary — verified directly that Cut correctly
// preserves/reopens any ANSI styling already present at the cut point
// (a colored line cut mid-span still carries its color on both sides),
// so wrapping the middle segment in reverse video doesn't clobber
// existing foreground colors, it layers on top the way real terminals
// apply SGR 7.
func applySelectionHighlight(content string, startLine, startCol, endLine, endCol int) string {
	lines := strings.Split(content, "\n")
	sl, sc, el, ec := normalizeSelection(startLine, startCol, endLine, endCol)
	sl, el, ok := selectionLineRange(len(lines), sl, el)
	if !ok {
		return content
	}

	for i := sl; i <= el; i++ {
		width := ansi.StringWidth(lines[i])
		left, right := 0, width
		if i == sl {
			left = sc
		}
		if i == el {
			right = ec
		}
		if left >= right {
			continue // nothing to highlight on this line (e.g. a zero-width click, not a drag)
		}
		before := ansi.Cut(lines[i], 0, left)
		mid := ansi.Cut(lines[i], left, right)
		after := ansi.Cut(lines[i], right, width)
		lines[i] = before + selReverseOn + mid + selReverseOff + after
	}
	return strings.Join(lines, "\n")
}
