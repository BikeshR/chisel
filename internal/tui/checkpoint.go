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
func (m Model) handleRewindCommand(args []string) (Model, tea.Cmd) {
	if m.checkpointStore == nil {
		m.appendLine(errorStyle.Render("checkpoints aren't available for this session"))
		return m, nil
	}
	if m.state != stateInput {
		m.appendLine(errorStyle.Render("can't rewind while a turn is in progress — press esc first"))
		return m, nil
	}

	if len(args) == 0 {
		return m.listCheckpoints(), nil
	}
	if args[0] == "confirm" {
		return m.confirmRewind()
	}

	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 || n > len(m.checkpoints) {
		m.appendLine(errorStyle.Render(fmt.Sprintf(
			"usage: /rewind <n> — %d checkpoint(s) available; /rewind alone lists them", len(m.checkpoints))))
		return m, nil
	}

	target := m.checkpoints[len(m.checkpoints)-n]
	m.pendingRewind = &target
	discarded := len(m.messages) - target.messageIndex
	m.appendLine(dimStyle.Render(fmt.Sprintf(
		"this will discard file changes since %q and remove %d message(s) from the conversation.", target.label, discarded)))
	m.appendLine(dimStyle.Render("type /rewind confirm to proceed, or anything else to cancel"))
	return m, nil
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

// handleDiffCommand implements /diff: the cumulative diff between the
// most recent checkpoint and the current working-directory state —
// complementing, not replacing, the per-edit preview diffs already
// shown in the permission prompt (colorizeDiff, shared with those).
// Uses the same session-scoped m.checkpoints /rewind already lists,
// not the shadow repo's full history, so this only ever compares
// against a point reachable from *this* conversation.
func (m Model) handleDiffCommand() Model {
	if m.checkpointStore == nil {
		m.appendLine(errorStyle.Render("checkpoints aren't available for this session"))
		return m
	}
	if len(m.checkpoints) == 0 {
		m.appendLine(dimStyle.Render("no checkpoints yet — one is taken automatically at the start of each turn"))
		return m
	}

	latest := m.checkpoints[len(m.checkpoints)-1]
	diff, err := m.checkpointStore.Diff(latest.hash)
	if err != nil {
		m.appendLine(errorStyle.Render("diff: " + err.Error()))
		return m
	}
	if strings.TrimSpace(diff) == "" {
		m.appendLine(dimStyle.Render(fmt.Sprintf("no changes since %q (%s)", latest.label, humanizeSince(latest.takenAt))))
		return m
	}
	m.appendLine(dimStyle.Render(fmt.Sprintf("changes since %q (%s):", latest.label, humanizeSince(latest.takenAt))) + "\n" + colorizeDiff(diff))
	return m
}

func (m Model) confirmRewind() (Model, tea.Cmd) {
	if m.pendingRewind == nil {
		m.appendLine(errorStyle.Render("nothing to confirm — use /rewind <n> first"))
		return m, nil
	}
	target := *m.pendingRewind
	m.pendingRewind = nil

	if err := m.checkpointStore.Restore(target.hash); err != nil {
		m.appendLine(errorStyle.Render("rewind failed: " + err.Error()))
		return m, nil
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
	// Same reasoning /new and /resume already apply (see their own doc
	// comments): the conversation just changed shape out from under
	// these, so an index describing a position in the pre-rewind
	// history (ctrl+o's target, the doom-loop counters) doesn't mean
	// anything here anymore.
	m.lastToolResultIdx = -1
	m.lastToolCallKey = ""
	m.toolCallRepeatCount = 0

	m.entries = renderHistory(m.messages)
	m.appendLine(dimStyle.Render(fmt.Sprintf("── rewound to %q ──", target.label)))
	m.refreshAndMaybeStickToBottom()

	// Persist the truncated conversation immediately — without this,
	// quitting right after a rewind resumed the *pre-rewind* conversation
	// against the *rewound* files on next launch, even though /help
	// promises "restore code+conversation", not just the code half.
	// Restore also just rewrote the working tree, so the cached status
	// bar's dirty/branch segment needs refreshing too, same as any other
	// file-changing action.
	return m, tea.Batch(saveSession(m.workDir, m.sessionID, m.messages), refreshGitStatus(m.workDir))
}
