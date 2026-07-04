package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/checkpoint"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
	"github.com/BikeshR/chisel/internal/session"
	"github.com/BikeshR/chisel/internal/skill"
)

// inputHeight is how many rows the multi-line input box shows — fixed
// rather than growing with content, so the rest of the layout (viewport
// height, in particular) doesn't need to be recomputed on every
// keystroke. Comfortably fits a pasted stack trace or a short code
// block without needing internal scrolling for most real input.
const inputHeight = 3

type state int

const (
	stateInput state = iota
	stateWaitingModel
	stateAwaitingPermission
	stateExecutingTool
)

// Model is chisel's Bubbletea model: it holds the conversation sent to the
// API, the queue of tool calls still to process for the in-flight turn, and
// enough UI state to render the transcript, a spinner, and a permission
// prompt.
type Model struct {
	client  *agent.Client
	workDir string
	bash    *agent.BashSession
	mcp     *mcp.Registry
	hooks   hooks.Config
	// memUser/memProject record which CHISEL.md files were found at
	// startup (see New) — kept only for /status to report later; the
	// content itself already went to the client via SetMemory before
	// this Model was ever built.
	memUser, memProject bool
	// customCommands are user-defined slash commands loaded at startup
	// (~/.chisel/commands/*.md and <workDir>/.chisel/commands/*.md) —
	// see handleCommand's default case, which checks here before
	// reporting a command as unknown.
	customCommands map[string]customcmd.Command
	// skills are user-defined skill files loaded at startup
	// (~/.chisel/skills/*.md and <workDir>/.chisel/skills/*.md) —
	// threaded into agent.Execute so the load_skill tool can look one
	// up by name. Names+descriptions also went to the client via
	// SetSkills before this Model was ever built, for the system prompt.
	skills map[string]skill.Skill

	messages []agent.Message
	entries  []entry // transcript, newest last — see transcript.go

	textArea textarea.Model
	viewport viewport.Model
	spinner  spinner.Model

	state          state
	pendingUses    []agent.ToolCall
	pendingResults []agent.Message // one "tool" role message per completed call

	// sessionAllowlist remembers "a" (always-allow) decisions from the
	// permission prompt for the rest of this session — see
	// permission.go's allowlistKey for what's eligible and why. In
	// memory only; nothing here is meant to persist across restarts.
	sessionAllowlist map[string]bool
	// awaitingDenialReason is set when "n" denies a tool call — instead
	// of immediately resending a canned denial message, the next thing
	// the user types (or nothing, if they just hit enter) becomes the
	// reason fed back to the model. See submit() in update.go.
	awaitingDenialReason bool
	// knownBrokenMCP records which MCP servers have already been
	// reported as broken and had their tools dropped from the client —
	// see syncMCPHealth — so a server that was already handled once
	// doesn't get re-announced (or re-attempt tool removal) every turn.
	knownBrokenMCP map[string]bool
	// queuedMessages holds text submitted (enter) while busy
	// (stateWaitingModel/stateExecutingTool) — delivered in order, one
	// at a time, by dequeueOrSubmit whenever chisel next returns to
	// stateInput, instead of being swallowed the way every keystroke
	// while busy used to be.
	queuedMessages []string
	// todos is the model's current task checklist, replaced wholesale
	// on every successful update_todos call (see parseTodos in todo.go)
	// — rendered as a persistent block in View, not appended to the
	// transcript, so it reads as a live status rather than a growing
	// log of every intermediate state.
	todos []agent.TodoItem

	// checkpointStore is nil if the shadow git repo couldn't be opened
	// (see checkpoint.Open) — /rewind reports "not available" rather
	// than chisel refusing to start over it. checkpoints records one
	// entry per turn, oldest first, tying each shadow-repo commit back
	// to a point in the conversation (see checkpointRecord). pendingRewind
	// holds the target set by "/rewind <n>" until "/rewind confirm" (or
	// a new turn starting) resolves it.
	checkpointStore *checkpoint.Store
	checkpoints     []checkpointRecord
	pendingRewind   *checkpointRecord

	// streamLineIdx is the index into entries of the assistant line
	// currently being built from streamed text deltas, or -1 if none is
	// in progress.
	streamLineIdx int
	streamText    string
	showThinking  bool // /think toggles this; collapsed by default
	autoCommit    bool // /git auto toggles this; off by default
	// preTurnDirty is a gitutil.DirtyPaths snapshot taken at the start of
	// each turn (see submit), so /git auto can commit only what changed
	// during it — never whatever the user already had unstaged before
	// chisel touched anything.
	preTurnDirty map[string]bool

	tokensIn, tokensOut int64
	// lastContextTokens is the prompt size of the most recent request —
	// unlike tokensIn (a running total across every request this session,
	// for cost tracking), this is "how full is the context window right
	// now", since every request resends the full history.
	lastContextTokens int64
	width, height     int
	quitting          bool

	// cancelTurn cancels whatever's currently in flight (a model request,
	// a tool call) — set by newTurnContext at the start of any async
	// operation, called by esc while busy (see handleKey). nil when
	// nothing is running.
	cancelTurn context.CancelFunc
}

// newTurnContext creates a fresh cancellable context for one async
// operation (a model request or a tool call) and stashes its cancel func
// so esc can abort it later. Cancelling a context already threaded
// through client.SendStreaming (via http.NewRequestWithContext) and
// BashSession.Run needs no further plumbing in either — both already
// respect ctx.Done(); this just makes sure a *real*, cancellable context
// reaches them instead of context.Background(). Any prior turn's cancel
// is called first — defensive, since the state machine shouldn't
// normally have two in flight at once, but leaking one would be a silent
// resource leak if that ever stopped being true.
func (m *Model) newTurnContext() context.Context {
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	return ctx
}

// endTurn clears the stashed cancel func once a turn is fully done —
// calling an already-fired CancelFunc again is harmless, but holding
// onto a stale one is just clutter once there's nothing left to cancel.
func (m *Model) endTurn() {
	m.cancelTurn = nil
}

// interruptibleErrorText renders err for display, showing a plain
// "interrupted" instead of the raw (fairly unfriendly) "context
// canceled" whenever esc is what actually caused it.
func interruptibleErrorText(err error) string {
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return err.Error()
}

// New builds the initial Model for a chisel session rooted at workDir.
// bash and mcpRegistry are owned by the caller (main.go), not created
// here, so their lifecycle (closing the shell / MCP server processes on
// exit) doesn't depend on anything inside this package. resumed and
// savedAt come from session.Load — pass a nil/zero pair if there's
// nothing to resume. hooksCfg comes from hooks.LoadConfig — a zero value
// is fine and just means no hooks configured. memUser/memProject report
// which CHISEL.md files memory.Load found, just to show a startup line —
// the content itself was already handed to the client via SetMemory.
// customCommands comes from customcmd.Load — a nil/empty map is fine and
// just means no custom commands are available. checkpointStore comes
// from checkpoint.Open — nil is fine and just means /rewind reports
// checkpoints as unavailable rather than chisel refusing to start.
// skills comes from skill.Load — a nil/empty map is fine and just means
// load_skill has nothing to find. Callers should also have already
// passed the same map to client.SetSkills before constructing here, so
// the system prompt and the tool's actual lookup stay in sync.
func New(client *agent.Client, workDir string, bash *agent.BashSession, mcpRegistry *mcp.Registry, hooksCfg hooks.Config, memUser, memProject bool, customCommands map[string]customcmd.Command, checkpointStore *checkpoint.Store, skills map[string]skill.Skill, resumed []agent.Message, savedAt time.Time) Model {
	ta := textarea.New()
	ta.Placeholder = "ask chisel to do something… (alt+enter for a new line, @path to reference a file, /help for commands)"
	ta.Focus()
	ta.CharLimit = 0 // unbounded — the whole point is supporting long pastes
	ta.ShowLineNumbers = false
	ta.SetHeight(inputHeight)
	// Enter is handleKey's own submit trigger (see stateInput), never
	// forwarded to the textarea — rebinding InsertNewline is what makes
	// alt+enter available for a literal newline instead of enter's
	// default (and, for this KeyMap, only) meaning.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter"))

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))

	vp := viewport.New(80, 20)
	vp.MouseWheelEnabled = true

	m := Model{
		client:          client,
		workDir:         workDir,
		bash:            bash,
		mcp:             mcpRegistry,
		hooks:           hooksCfg,
		memUser:         memUser,
		memProject:      memProject,
		customCommands:  customCommands,
		checkpointStore: checkpointStore,
		skills:          skills,
		messages:        resumed,
		textArea:        ta,
		viewport:        vp,
		spinner:         sp,
		state:           stateInput,
		streamLineIdx:   -1,
	}

	if memUser || memProject {
		m.entries = append(m.entries, entry{styled: dimStyle.Render("loaded " + memoryBannerText(memUser, memProject))})
	}

	if len(resumed) > 0 {
		m.entries = append(m.entries, entry{styled: resumeBanner(len(resumed), savedAt)})
		m.entries = append(m.entries, renderHistory(resumed)...)
	}

	if len(m.entries) > 0 {
		m.refreshViewport()
		m.viewport.GotoBottom()
	}

	return m
}

func memoryBannerText(memUser, memProject bool) string {
	switch {
	case memUser && memProject:
		return "CHISEL.md (user + project)"
	case memProject:
		return "CHISEL.md (project)"
	default:
		return "CHISEL.md (user)"
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m *Model) appendLine(s string) {
	m.entries = append(m.entries, entry{styled: s})
	m.refreshAndMaybeStickToBottom()
}

// appendAssistantEntry is appendLine's counterpart for text that should
// be re-collapsible/expandable by /think — see entry.isAssistant.
func (m *Model) appendAssistantEntry(raw string) {
	m.entries = append(m.entries, entry{isAssistant: true, raw: raw})
	m.refreshAndMaybeStickToBottom()
}

// appendStreamText appends a text delta to the assistant line currently
// being streamed, starting a new line on the first delta of a turn.
func (m *Model) appendStreamText(delta string) {
	if m.streamLineIdx == -1 {
		m.entries = append(m.entries, entry{isAssistant: true})
		m.streamLineIdx = len(m.entries) - 1
		m.streamText = ""
	}
	m.streamText += delta
	m.entries[m.streamLineIdx].raw = m.streamText
	m.refreshAndMaybeStickToBottom()
}

// refreshAndMaybeStickToBottom rebuilds the viewport's content and keeps
// the view pinned to the bottom only if it already was there — once the
// user has scrolled up (rereading something, or a permission prompt's
// diff too long to fit on screen), new content streaming in shouldn't
// yank them back down to the end.
func (m *Model) refreshAndMaybeStickToBottom() {
	stuck := m.viewport.AtBottom()
	m.refreshViewport()
	if stuck {
		m.viewport.GotoBottom()
	}
}

// recomputeViewportHeight sets the transcript viewport's height from
// the current terminal size, minus the fixed input-box-and-status-bar
// margin and however many lines the todo block currently needs — unlike
// the input box, the todo block's height isn't fixed, so this has to be
// redone whenever the todo list changes, not just on resize.
func (m *Model) recomputeViewportHeight() {
	extra := inputHeight + 3 // input box + status bar + margin
	if n := len(m.todos); n > 0 {
		extra += n + 1 // the todo block itself, plus the blank line separating it from the transcript
	}
	m.viewport.Height = m.height - extra
}

// syncMCPHealth checks for MCP servers that have newly gone broken
// (see mcp.Server.markBroken) since the last check, and for each:
// drops its tools from the client (there's no point continuing to
// offer the model a tool that will just fail every time) and reports
// it once in the transcript. Without this, a dead server's tools stayed
// in every request indefinitely and the model kept trying them with no
// indication anything was wrong — only ever visible via /status,
// checked on demand, not surfaced proactively. Cheap enough (an
// in-memory map lookup per configured server) to call at the start of
// every turn rather than needing a background poller.
func (m *Model) syncMCPHealth() {
	for _, s := range m.mcp.Statuses() {
		if !s.Broken || m.knownBrokenMCP[s.Name] {
			continue
		}
		if m.knownBrokenMCP == nil {
			m.knownBrokenMCP = make(map[string]bool)
		}
		m.knownBrokenMCP[s.Name] = true
		m.client.RemoveToolsWithPrefix("mcp__" + s.Name + "__")
		m.appendLine(errorStyle.Render(fmt.Sprintf("mcp: %s is no longer responding — its tools have been removed for this session", s.Name)))
	}
}

// endStreamLine closes out the in-progress assistant line so the next text
// block (if any, within the same or a later turn) starts a fresh one.
func (m *Model) endStreamLine() {
	m.streamLineIdx = -1
	m.streamText = ""
}

// executeTool runs a tool call's full lifecycle: preToolUse hooks (which
// can block it outright), the call itself, then postToolUse hooks (whose
// output, if any, is folded into the result so the model sees it). Hooks
// run here rather than as a separate pre-permission-prompt step because
// they're arbitrary shell commands that can take real time (up to
// hooks.hookTimeout) — unlike plan mode's block, which is a plain boolean
// check and cheap enough to do synchronously before the prompt even
// appears, hooks have to go through the same async Cmd as the tool call
// itself. The tradeoff: a hook can still block a call the user already
// approved via the permission prompt, rather than pre-empting the prompt
// entirely — accepted for the simplicity of not needing a second async
// round-trip before every permission decision.
func executeTool(ctx context.Context, workDir, model string, bash *agent.BashSession, mcpRegistry *mcp.Registry, hooksCfg hooks.Config, skills map[string]skill.Skill, call agent.ToolCall) tea.Cmd {
	return func() tea.Msg {
		path := toolPath(call)

		blocked, reason, err := hooks.RunPreToolUse(ctx, workDir, hooksCfg.Hooks.PreToolUse, call.Function.Name, call.Function.Arguments, path)
		if err != nil {
			return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: "pre-tool-use hook: " + err.Error(), IsError: true}}
		}
		if blocked {
			return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: "Blocked by a preToolUse hook: " + reason, IsError: true}}
		}

		var result agent.ToolResult
		if mcp.IsToolName(call.Function.Name) {
			args := json.RawMessage(call.Function.Arguments)
			content, isError, err := mcpRegistry.Call(ctx, call.Function.Name, args)
			if err != nil {
				result = agent.ToolResult{ID: call.ID, Content: err.Error(), IsError: true}
			} else {
				result = agent.ToolResult{ID: call.ID, Content: content, IsError: isError}
			}
		} else {
			result = agent.Execute(ctx, workDir, model, call, bash, skills)
		}

		if !result.IsError {
			if out, err := hooks.RunPostToolUse(ctx, workDir, hooksCfg.Hooks.PostToolUse, call.Function.Name, call.Function.Arguments, path); err != nil {
				result.Content += "\n\n(post-tool-use hook: " + err.Error() + ")"
			} else if out != "" {
				result.Content += "\n\n[hook] " + out
			}
		}

		return toolResultMsg{result: result}
	}
}

// toolPath pulls a "path" argument out of call, if it has one — chisel's
// editor tool always does, and this stays generic (any tool with a "path"
// argument benefits) rather than special-casing by tool name.
func toolPath(call agent.ToolCall) string {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &in)
	return in.Path
}

// summarizeCall renders a permission-prompt-friendly description of call,
// prettifying chisel's mcp__server__tool naming into "server: tool" —
// agent.Summarize itself doesn't know about that convention, by design
// (see internal/mcp's package doc), so this is purely a display-layer
// improvement on top of it.
func summarizeCall(call agent.ToolCall) string {
	if server, tool, ok := mcp.SplitToolName(call.Function.Name); ok {
		return fmt.Sprintf("%s: %s", server, tool)
	}
	return agent.Summarize(call)
}

// saveSession persists messages as the current session for workDir. A
// failure here isn't fatal to the conversation itself, so it's reported
// as a sessionSaveErrorMsg rather than surfaced through the normal
// error-handling path.
func saveSession(workDir string, messages []agent.Message) tea.Cmd {
	return func() tea.Msg {
		if err := session.Save(workDir, messages); err != nil {
			return sessionSaveErrorMsg{err: err}
		}
		return nil
	}
}

// autoCommit stages and commits whatever changed since preTurnDirty was
// captured, if /git auto is on — never a blanket `git add -A`, which
// would also sweep up any unrelated work the user already had unstaged
// in the same working tree before this turn started. Returning a nil Msg
// (via the early returns below) is deliberate — "nothing to commit" and
// "not a repo" aren't events worth a line in the transcript every turn.
func autoCommit(workDir string, preTurnDirty map[string]bool, userText string) tea.Cmd {
	return func() tea.Msg {
		if !gitutil.IsRepo(workDir) {
			return nil
		}
		sha, err := gitutil.CommitNewlyChanged(workDir, preTurnDirty, commitMessage(userText))
		if err != nil {
			return autoCommitResultMsg{err: err}
		}
		if sha == "" {
			return nil
		}
		return autoCommitResultMsg{sha: sha}
	}
}

// commitMessage derives a short, git-subject-line-length message from the
// user request that drove this turn's changes.
func commitMessage(userText string) string {
	const maxLen = 72
	subject := firstLine(userText)
	if truncated, ok := truncateRunes(subject, maxLen); ok {
		subject = truncated + "…"
	}
	return "chisel: " + subject
}

// truncateRunes cuts s to at most maxRunes runes. Slicing a string by
// byte count (s[:n]) can land in the middle of a multi-byte UTF-8
// character and produce invalid, garbled output — this always cuts on a
// rune boundary instead. ok reports whether s was actually longer than
// maxRunes (so callers know whether to append their own "truncated"
// marker).
func truncateRunes(s string, maxRunes int) (truncated string, ok bool) {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s, false
	}
	return string(runes[:maxRunes]), true
}

// lastUserText returns the most recent user message in messages, for
// deriving an auto-commit message. Falls back to a generic subject if
// there somehow isn't one (shouldn't happen in practice — every turn
// starts with a user message).
func lastUserText(messages []agent.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return "changes"
}

// compactPrompt asks the model to summarize the conversation so far, for
// /compact. Sent as one more turn through the same client — chisel has no
// server-side compaction to lean on (that's an Anthropic API feature),
// so this is the model doing the summarizing itself.
const compactPrompt = "Summarize this conversation so far in a concise form for continuing the work later: the overall goal, key decisions made, files created or modified and how, and anything still unresolved. Skip narration and pleasantries — just the substance needed to pick back up."

// compact sends messages plus the compaction instruction and returns the
// model's summary. Uses client.WithoutTools() — this request has no
// legitimate reason to call any tool, and a model that decided to call
// one anyway (tool-happy models asked to "summarize... files created or
// modified" are a real, not just hypothetical, failure mode) would
// otherwise return empty content that silently replaces the whole
// conversation with nothing. The explicit check below is the backstop
// for a model that ignores the empty tool set and calls one anyway.
func compact(ctx context.Context, client *agent.Client, messages []agent.Message) tea.Cmd {
	return func() tea.Msg {
		history := append(append([]agent.Message{}, messages...), agent.Message{Role: "user", Content: compactPrompt})

		ch, err := client.WithoutTools().SendStreaming(ctx, history)
		if err != nil {
			return compactResultMsg{err: err}
		}

		msg, usage, err := agent.Drain(ch)
		if err != nil {
			return compactResultMsg{err: err}
		}
		if msg.Content == "" || len(msg.ToolCalls) > 0 {
			return compactResultMsg{
				err:   errors.New("model returned no plain-text summary (empty content or a tool call) — the conversation was left untouched; try again or use /new instead"),
				usage: usage,
			}
		}
		return compactResultMsg{summary: msg.Content, usage: usage}
	}
}

// compactedHistory replaces the full conversation with a single message
// carrying the model's own summary of it, framed as background for
// whatever comes next.
func compactedHistory(summary string) []agent.Message {
	return []agent.Message{
		{Role: "user", Content: "Here is a summary of our conversation so far, before it was compacted to save context:\n\n" + summary + "\n\nContinue from here."},
	}
}

// contextWarnThreshold is a conservative, deliberately generic rule of
// thumb — chisel doesn't maintain a per-model context-window table (the
// OpenCode Go catalog changes, and getting a specific model's exact limit
// wrong would be worse than not claiming one at all), so this just flags
// "this is getting large" rather than "you are at N% of this model's
// limit".
const contextWarnThreshold = 100_000

// formatTokenCount renders a token count compactly for the status bar.
func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
