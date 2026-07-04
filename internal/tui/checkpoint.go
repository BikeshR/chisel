package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/checkpoint"
)

// checkpointRecord ties a shadow-repo commit back to a point in the
// conversation. messageIndex is len(m.messages) at the moment the
// checkpoint was taken — i.e. before that turn's user message was
// appended — so restoring to it means truncating m.messages back to
// exactly that length.
type checkpointRecord struct {
	hash         string
	label        string
	messageIndex int
	takenAt      time.Time
}

// checkpointCreatedMsg carries the outcome of an async checkpoint
// commit taken at the start of a turn — see checkpointCmd.
type checkpointCreatedMsg struct {
	record checkpointRecord
	err    error
}

// checkpointCmd snapshots the working directory into the shadow repo
// in the background, concurrently with the model request that's about
// to start. There's no need to block submitText on this: nothing can
// actually modify a file until a tool call resolves, which is always
// at least one full model round-trip away — plenty of time for a git
// commit to finish first. Returns a nil Cmd if store is nil (no
// project directory could support one, or Open failed at startup).
func checkpointCmd(store *checkpoint.Store, label string, messageIndex int) tea.Cmd {
	if store == nil {
		return nil
	}
	return func() tea.Msg {
		hash, err := store.Checkpoint(label)
		if err != nil {
			return checkpointCreatedMsg{err: err}
		}
		return checkpointCreatedMsg{record: checkpointRecord{
			hash: hash, label: label, messageIndex: messageIndex, takenAt: time.Now(),
		}}
	}
}

// handleCheckpointCreated records a completed background checkpoint. A
// failure here is best-effort and doesn't interrupt the turn — it just
// means /rewind has one fewer point to restore to for this one, a much
// smaller loss than a failed session save, so it's reported quietly.
func (m Model) handleCheckpointCreated(msg checkpointCreatedMsg) Model {
	if msg.err != nil {
		m.appendLine(dimStyle.Render("checkpoint failed: " + msg.err.Error()))
		return m
	}
	m.checkpoints = append(m.checkpoints, msg.record)
	return m
}

// handleRewindCommand implements /rewind [n] / /rewind confirm.
// Restricted to stateInput: restoring file state and truncating the
// conversation mid-turn (pending tool calls, an in-flight stream) has
// no sane meaning, so it's simplest to just require the turn be over
// or interrupted first.
func (m Model) handleRewindCommand(args []string) Model {
	if m.checkpointStore == nil {
		m.appendLine(errorStyle.Render("checkpoints aren't available for this session"))
		return m
	}
	if m.state != stateInput {
		m.appendLine(errorStyle.Render("can't rewind while a turn is in progress — press esc first"))
		return m
	}

	if len(args) == 0 {
		return m.listCheckpoints()
	}
	if args[0] == "confirm" {
		return m.confirmRewind()
	}

	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 || n > len(m.checkpoints) {
		m.appendLine(errorStyle.Render(fmt.Sprintf(
			"usage: /rewind <n> — %d checkpoint(s) available; /rewind alone lists them", len(m.checkpoints))))
		return m
	}

	target := m.checkpoints[len(m.checkpoints)-n]
	m.pendingRewind = &target
	discarded := len(m.messages) - target.messageIndex
	m.appendLine(dimStyle.Render(fmt.Sprintf(
		"this will discard file changes since %q and remove %d message(s) from the conversation.", target.label, discarded)))
	m.appendLine(dimStyle.Render("type /rewind confirm to proceed, or anything else to cancel"))
	return m
}

func (m Model) listCheckpoints() Model {
	if len(m.checkpoints) == 0 {
		m.appendLine(dimStyle.Render("no checkpoints yet — one is taken automatically at the start of each turn"))
		return m
	}

	var b strings.Builder
	b.WriteString("checkpoints (most recent first):\n")
	for i := len(m.checkpoints) - 1; i >= 0; i-- {
		n := len(m.checkpoints) - i
		c := m.checkpoints[i]
		fmt.Fprintf(&b, "  %d. %s (%s)\n", n, c.label, humanizeSince(c.takenAt))
	}
	b.WriteString("type /rewind <n> to restore to that point")
	m.appendLine(dimStyle.Render(b.String()))
	return m
}

func (m Model) confirmRewind() Model {
	if m.pendingRewind == nil {
		m.appendLine(errorStyle.Render("nothing to confirm — use /rewind <n> first"))
		return m
	}
	target := *m.pendingRewind
	m.pendingRewind = nil

	if err := m.checkpointStore.Restore(target.hash); err != nil {
		m.appendLine(errorStyle.Render("rewind failed: " + err.Error()))
		return m
	}

	m.messages = m.messages[:target.messageIndex]
	// Anything checkpointed after the one just restored to no longer
	// corresponds to a reachable state — the restored checkpoint is now
	// the most recent one.
	for i, c := range m.checkpoints {
		if c.hash == target.hash {
			m.checkpoints = m.checkpoints[:i+1]
			break
		}
	}

	m.entries = renderHistory(m.messages)
	m.appendLine(dimStyle.Render(fmt.Sprintf("── rewound to %q ──", target.label)))
	m.refreshAndMaybeStickToBottom()
	return m
}
