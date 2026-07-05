package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	"github.com/BikeshR/chisel/internal/history"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
	"github.com/BikeshR/chisel/internal/permrules"
	"github.com/BikeshR/chisel/internal/session"
	"github.com/BikeshR/chisel/internal/skill"
	"github.com/BikeshR/chisel/internal/subagentdef"
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
	// sessionID is which of workDir's (possibly several) saved sessions
	// this conversation saves back to — see internal/session. Set once
	// at startup (the resumed session's own ID, or a freshly minted one),
	// changed by /new (a new ID, abandoning the old one rather than
	// deleting it) and /resume (switching to a past session's own ID).
	sessionID string
	permRules permrules.Config
	// memUser/memProject record whether memory.Load found anything at
	// the user level (~/.chisel/CHISEL.md) and project level
	// (<workDir>/AGENTS.md and/or CHISEL.md) at startup (see New) —
	// kept only for /status to report later; the content itself already
	// went to the client via SetMemory before this Model was ever built.
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
	// subagents are user-defined custom subagent roles loaded at
	// startup (~/.chisel/agents/*.md and <workDir>/.chisel/agents/*.md,
	// see internal/subagentdef) — threaded into agent.Execute so
	// dispatch_subagent can resolve its optional "agent" role name.
	// Names+descriptions also went to the client via SetSubagents
	// before this Model was ever built, folded into that tool's schema.
	subagents map[string]subagentdef.Subagent

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
	// lastToolCallKey/toolCallRepeatCount track consecutive identical
	// tool calls (see permission.go's toolCallKey/doomLoopThreshold) —
	// reset whenever a call differs from the previous one, regardless
	// of turn boundaries, since a model stuck repeating the same call
	// can do so across several turns just as easily as within one.
	lastToolCallKey     string
	toolCallRepeatCount int
	// awaitingLoopConfirmation is set when the current permission prompt
	// (stateAwaitingPermission) was forced by the doom-loop guard rather
	// than a normal ask — checked by the "a" key handler so a habitual
	// "always allow" doesn't silently permit every future repeat of a
	// call that's actively suspected of looping, even for a call that
	// would otherwise be allowlist-eligible.
	awaitingLoopConfirmation bool
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
	// inputHistory records every submitted (or queued) line, oldest
	// first, for up/down recall — see navigateHistory in update.go.
	// historyIdx is the index currently shown, or -1 when not navigating
	// (the textarea holds whatever the user is actively composing).
	// historyDraft is that in-progress composition, stashed on the first
	// "up" so "down" back past the most recent entry can restore it,
	// mirroring a shell's history behavior.
	inputHistory []string
	historyIdx   int
	historyDraft string
	// reverseSearchActive is true while ctrl+r's incremental reverse
	// search is active — a sub-mode layered on top of stateInput (the
	// same pattern awaitingDenialReason already uses) rather than a new
	// top-level state, since it's purely about how the textarea's
	// content gets composed, not the agent loop. reverseSearchQuery is
	// what's been typed so far; reverseSearchMatchIdx is the inputHistory
	// index of the current match, or -1 if the query matches nothing.
	reverseSearchActive   bool
	reverseSearchQuery    string
	reverseSearchMatchIdx int

	// commandPaletteCandidates is the live-filtered list of "/"-command
	// names matching whatever's currently being typed — see
	// refreshCommandPalette, called after every keystroke that could
	// have changed the textarea while composing a bare slash command
	// (the same "first token, no space yet" condition tab-completion
	// already used). Its presence (non-nil) is what View checks to
	// decide whether to render the dropdown at all, and what handleKey
	// checks to steer up/down/tab/enter at the palette instead of their
	// usual meaning (history recall, file-ref completion, submit).
	// commandPaletteSelected indexes into it for the highlighted row.
	commandPaletteCandidates []string
	commandPaletteSelected   int

	// modelPickerActive is set when a bare "/model" (no further args) is
	// submitted — an interactive alternative to just printing every
	// known model as static text, letting the user arrow to one and
	// press enter to switch immediately rather than retyping its exact
	// name as a separate command. A sub-mode of stateInput, the same
	// pattern reverseSearchActive already uses, since nothing async is
	// happening — it's purely how the input area is being used right
	// now. modelPickerSelected indexes into agent.KnownModels().
	modelPickerActive   bool
	modelPickerSelected int

	// goal is a standing condition set by /goal — when a turn ends with
	// no more tool calls and nothing else is queued, handleStreamComplete
	// auto-submits a "keep going" follow-up instead of returning to idle,
	// so a multi-turn task doesn't need "continue" retyped after every
	// turn. Empty means no goal is set (the default, ordinary behavior).
	// This never weakens the permission gate — every tool call the model
	// makes while pursuing the goal still goes through the exact same
	// y/n prompt as any other turn; it only automates the re-prompting.
	// goalContinuations counts consecutive auto-continuations since the
	// goal was last set or the user last submitted something themselves
	// (see submit(), which resets it) — capped by maxGoalContinuations
	// so an unbounded loop can't run away silently.
	goal              string
	goalContinuations int
	// lastAssistantText/assistantTextRepeatCount track consecutive
	// turns where the assistant's own final text response came back
	// identical — opencode's 2026 extension of the same doom-loop idea
	// lastToolCallKey/toolCallRepeatCount already apply to repeated tool
	// calls (see permission.go's doomLoopThreshold), applied here to
	// repeated *output* instead of repeated *actions*. Most valuable
	// against /goal's auto-continuation specifically: without this, a
	// model that's already stuck saying the same thing verbatim would
	// keep getting re-prompted to "continue" until maxGoalContinuations
	// ran out, for zero actual progress. Reset on a real user
	// submission (see submit()) the same way goalContinuations is.
	lastAssistantText        string
	assistantTextRepeatCount int

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

	// backgroundTasks tracks commands started via bash_background,
	// keyed by task ID — see background.go. Independent of any turn:
	// a task's own context isn't cancelled by esc or a turn ending,
	// only by CancelBackgroundTasks (chisel exiting) or the command
	// finishing on its own.
	backgroundTasks map[string]*backgroundTask
	// pendingBackgroundResults holds a finished background task's
	// synthetic message when it completes *mid-turn* — appending it to
	// m.messages immediately would risk landing it between an
	// assistant's tool_calls and their own results (whichever provider
	// rejects that shape), if the timing is unlucky. Held here instead
	// and merged in once the current turn is fully resolved — see
	// mergeBufferedBackgroundResults, called from every chokepoint that
	// already means "the turn just settled" (dequeueOrSubmit's callers).
	pendingBackgroundResults []agent.Message

	// lastToolResultIdx is the entries index of the most recently
	// appended tool-result entry, or -1 if none yet — ctrl+o
	// (toggleLastToolResult) expands/collapses that one entry.
	lastToolResultIdx int

	// selecting is true while a left-button mouse drag is selecting text
	// over the transcript viewport — see handleMouseMsg (selection.go).
	// selStart*/selEnd* mark its current extent as (line, col) pairs,
	// line indexing into the full wrapped transcript content, not just
	// what's currently scrolled into view.
	selecting                 bool
	selStartLine, selStartCol int
	selEndLine, selEndCol     int
	// pendingClipboardOSC is a raw OSC-52 clipboard-set escape sequence
	// queued to go out with the very next rendered frame (see View) —
	// cleared by clearClipboardOSCMsg once it's had that one render cycle.
	pendingClipboardOSC string
	// pendingNotifyOSC is a raw bell+OSC-9 desktop-notification escape
	// sequence, queued and rendered the same way pendingClipboardOSC is
	// (see notifyIdle) rather than written to os.Stdout directly from a
	// Cmd goroutine — the exact race the OSC-52 code above documents
	// avoiding. Cleared by clearNotifyOSCMsg after one render cycle.
	pendingNotifyOSC string

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

	// gitIsRepo/gitBranch/gitDirty cache the status bar's branch/dirty
	// segment — refreshed once at startup and once per completed turn
	// (see refreshGitStatus), not on every render: git rev-parse/status
	// are subprocess calls, and View() runs many times a second while
	// streaming, so shelling out on every render would be wasteful for a
	// value that only changes at the pace of actual file edits.
	gitIsRepo bool
	gitBranch string
	gitDirty  bool

	// turnStartedAt marks when the current busy state (stateWaitingModel or
	// stateExecutingTool) began — see startBusy — so the spinner line can
	// show elapsed time instead of a static "thinking…"/"running…" label.
	turnStartedAt time.Time
	// permissionHint is the y/n/a(+esc) options string for the prompt
	// currently on screen, computed once in dispatchNextTool alongside the
	// prompt text itself — View() renders this instead of a hardcoded
	// "(y/n)" that didn't always match what was actually offered.
	permissionHint string

	tokensIn, tokensOut int64
	// requestCount counts real API round-trips this session — a normal
	// turn, a /compact, or a dispatch_subagent call (undercounted
	// slightly for the last one: a subagent can make several internal
	// requests bundled into the one Usage value its caller sees, so
	// this counts that as one). See handleUsageCommand — shown alongside
	// tokensIn/tokensOut rather than a dollar estimate, since OpenCode
	// Go's subscription doesn't expose real cost data to estimate one
	// from (see /usage's own doc comment).
	requestCount int64
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

// startBusy transitions into a busy state (stateWaitingModel or
// stateExecutingTool) and stamps when it began, so the spinner line can
// show elapsed time — see turnStartedAt.
func (m *Model) startBusy(s state) {
	m.state = s
	m.turnStartedAt = time.Now()
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
// savedAt come from session.LoadLatest — pass a nil/zero pair if there's
// nothing to resume. sessionID is whichever session future saves go to
// — LoadLatest's own resumed ID, or a freshly minted session.NewID() if
// there was nothing to resume — since a session can't be saved back to
// without one. hooksCfg comes from hooks.LoadConfig — a zero value
// is fine and just means no hooks configured. memUser/memProject report
// whether memory.Load found anything at each level, just to show a
// startup line — the content itself was already handed to the client
// via SetMemory.
// customCommands comes from customcmd.Load — a nil/empty map is fine and
// just means no custom commands are available. checkpointStore comes
// from checkpoint.Open — nil is fine and just means /rewind reports
// checkpoints as unavailable rather than chisel refusing to start.
// skills comes from skill.Load — a nil/empty map is fine and just means
// load_skill has nothing to find. Callers should also have already
// passed the same map to client.SetSkills before constructing here, so
// the system prompt and the tool's actual lookup stay in sync. subagents
// comes from subagentdef.Load — a nil/empty map is fine and just means
// dispatch_subagent only offers its single built-in role; the same
// sync requirement applies, via client.SetSubagents. permRules
// comes from permrules.Load — a nil/empty Config is fine and just means
// no persistent rules are configured, same as an absent hooks.json.
// sessionLoadFailed comes from session.LoadLatest's own corrupt return
// value — true only when a previous session file existed but couldn't
// be read or parsed, as opposed to there simply being no prior session
// yet; it drives a one-line warning so a corrupt session doesn't just
// silently vanish with no indication anything went wrong.
// sessionStartOutput comes from hooks.RunSessionStart, already run by
// the caller before constructing here — empty if no SessionStart hooks
// are configured or none printed anything; shown as a startup line the
// same way the memory/session banners are.
func New(client *agent.Client, workDir string, bash *agent.BashSession, mcpRegistry *mcp.Registry, hooksCfg hooks.Config, memUser, memProject bool, customCommands map[string]customcmd.Command, checkpointStore *checkpoint.Store, skills map[string]skill.Skill, subagents map[string]subagentdef.Subagent, permRules permrules.Config, resumed []agent.Message, savedAt time.Time, sessionLoadFailed bool, sessionID string, sessionStartOutput string) Model {
	ta := textarea.New()
	ta.Placeholder = "ask chisel to do something… (alt+enter for a new line, @path to reference a file, !cmd to run a shell command directly, /help for commands)"
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
		client:            client,
		workDir:           workDir,
		sessionID:         sessionID,
		bash:              bash,
		mcp:               mcpRegistry,
		hooks:             hooksCfg,
		permRules:         permRules,
		memUser:           memUser,
		memProject:        memProject,
		customCommands:    customCommands,
		checkpointStore:   checkpointStore,
		skills:            skills,
		subagents:         subagents,
		messages:          resumed,
		textArea:          ta,
		viewport:          vp,
		spinner:           sp,
		state:             stateInput,
		streamLineIdx:     -1,
		historyIdx:        -1,
		lastToolResultIdx: -1,
		inputHistory:      history.Load(),
	}

	if memUser || memProject {
		m.entries = append(m.entries, entry{styled: dimStyle.Render("loaded " + memoryBannerText(memUser, memProject))})
	}

	if sessionStartOutput != "" {
		m.entries = append(m.entries, entry{styled: dimStyle.Render("[sessionStart hook] " + sessionStartOutput)})
	}

	if sessionLoadFailed {
		m.entries = append(m.entries, entry{styled: errorStyle.Render("the saved session for this directory couldn't be read — starting fresh; the old file is left untouched in ~/.chisel/sessions")})
	}

	if len(resumed) > 0 {
		m.entries = append(m.entries, entry{styled: resumeBanner(len(resumed), savedAt)})
		m.entries = append(m.entries, renderHistory(resumed)...)
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].isToolResult {
				m.lastToolResultIdx = i
				break
			}
		}
	}

	if len(m.entries) > 0 {
		m.refreshViewport()
		m.viewport.GotoBottom()
	}

	return m
}

// memoryBannerText doesn't say "CHISEL.md" unconditionally for the
// project layer — memory.Load also reads a project's AGENTS.md
// alongside (or instead of) CHISEL.md, and this can't tell from just
// the two bools which one actually contributed.
func memoryBannerText(memUser, memProject bool) string {
	switch {
	case memUser && memProject:
		return "CHISEL.md (user) + project instructions"
	case memProject:
		return "project instructions (AGENTS.md/CHISEL.md)"
	default:
		return "CHISEL.md (user)"
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick, refreshGitStatus(m.workDir))
}

// gitStatusMsg carries a refreshed git branch/dirty snapshot for the
// status bar — see refreshGitStatus and Model.gitIsRepo/gitBranch/gitDirty.
type gitStatusMsg struct {
	isRepo bool
	branch string
	dirty  bool
}

// refreshGitStatus shells out to git (rev-parse + status --porcelain)
// once, off the render path — called at startup and after each
// completed turn (see handleStreamComplete), not on every View(), since
// that runs many times a second while streaming and a value that only
// changes at the pace of actual file edits doesn't need refreshing that
// often.
func refreshGitStatus(workDir string) tea.Cmd {
	return func() tea.Msg {
		if !gitutil.IsRepo(workDir) {
			return gitStatusMsg{}
		}
		branch, _ := gitutil.Branch(workDir)
		dirty, _ := gitutil.DirtyPaths(workDir)
		return gitStatusMsg{isRepo: true, branch: branch, dirty: len(dirty) > 0}
	}
}

func (m *Model) appendLine(s string) {
	m.entries = append(m.entries, entry{styled: s})
	m.refreshAndMaybeStickToBottom()
}

// appendPermissionLine appends an already-bordered permission-prompt box,
// marked noRewrap so transcriptContent doesn't re-wrap (and mangle) its
// border — see entry.noRewrap.
func (m *Model) appendPermissionLine(s string) {
	m.entries = append(m.entries, entry{styled: s, noRewrap: true})
	m.refreshAndMaybeStickToBottom()
}

// appendToolResultEntry appends a tool call's result as a collapsed
// (first-line-only) entry, remembering its index so a following ctrl+o
// (toggleLastToolResult) can expand it — see entry.isToolResult.
func (m *Model) appendToolResultEntry(content string, isErr bool) {
	m.entries = append(m.entries, entry{isToolResult: true, fullContent: content, resultIsErr: isErr})
	m.lastToolResultIdx = len(m.entries) - 1
	m.refreshAndMaybeStickToBottom()
}

// toggleLastToolResult expands or collapses the most recent tool-result
// entry (see appendToolResultEntry) — a no-op if there isn't one yet.
func (m *Model) toggleLastToolResult() {
	if m.lastToolResultIdx < 0 || m.lastToolResultIdx >= len(m.entries) {
		return
	}
	if !m.entries[m.lastToolResultIdx].isToolResult {
		return
	}
	m.entries[m.lastToolResultIdx].expanded = !m.entries[m.lastToolResultIdx].expanded
	m.entries[m.lastToolResultIdx].cacheValid = false
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
		m.entries = append(m.entries, entry{isAssistant: true, streaming: true})
		m.streamLineIdx = len(m.entries) - 1
		m.streamText = ""
	}
	m.streamText += delta
	m.entries[m.streamLineIdx].raw = m.streamText
	m.entries[m.streamLineIdx].cacheValid = false // content just changed; see entry's own doc comment
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
// margin and however many lines the todo block, the command palette
// dropdown, or the model picker currently need — unlike the input box,
// none of those have a fixed height, so this has to be redone whenever
// any of them change, not just on resize. Measures the todo block's
// actual wrapped line count rather than assuming one row per item — a
// long item wraps to more than one terminal row at m.width, and
// undercounting it here pushes the input box/status bar off-screen.
func (m *Model) recomputeViewportHeight() {
	extra := inputHeight + 3 // input box + status bar + margin
	if todos := wrapToWidth(renderTodos(m.todos), m.width); todos != "" {
		extra += strings.Count(todos, "\n") + 2 // the todo block itself, plus the blank line separating it from the transcript
	}
	switch {
	case m.modelPickerActive:
		// Replaces the fixed-height textarea outright with a list as
		// tall as agent.KnownModels() plus its own header line, rather
		// than sitting on top of it the way the todo block and the
		// command palette do — subtract inputHeight back out first.
		extra -= inputHeight
		extra += len(agent.KnownModels()) + 1
	case len(m.commandPaletteCandidates) > 0:
		extra += len(m.commandPaletteCandidates) + 1 // the dropdown itself, plus the blank line separating it from the textarea
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
// Marks the just-finished entry no longer streaming and invalidates its
// cache — now that it's complete, the next render is what first makes it
// eligible for markdown rendering (see renderMarkdownEntry), a different
// result than whatever the streaming-plain-text render last cached.
func (m *Model) endStreamLine() {
	if m.streamLineIdx != -1 && m.streamLineIdx < len(m.entries) {
		m.entries[m.streamLineIdx].streaming = false
		m.entries[m.streamLineIdx].cacheValid = false
	}
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
func executeTool(ctx context.Context, workDir, model string, bash *agent.BashSession, mcpRegistry *mcp.Registry, hooksCfg hooks.Config, skills map[string]skill.Skill, subagents map[string]subagentdef.Subagent, plannerModel string, call agent.ToolCall) tea.Cmd {
	// bash_background needs to return a tea.Batch (the "started" result,
	// a record for Update to track, and a watcher that fires much
	// later, whenever the command actually finishes) rather than a
	// single Msg — outside the uniform "run it, get one result back"
	// shape every other case here follows, so it's split out before the
	// closure below rather than squeezed into it. preToolUse hooks
	// still apply (still an arbitrary shell command); postToolUse
	// doesn't, since there's no sensible way to run one against output
	// that isn't available until long after this call returns.
	if call.Function.Name == "bash_background" {
		return func() tea.Msg {
			path := toolPath(call)
			blocked, reason, err := hooks.RunPreToolUse(ctx, workDir, hooksCfg.Hooks.PreToolUse, call.Function.Name, call.Function.Arguments, path)
			if err != nil {
				return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: "pre-tool-use hook: " + err.Error(), IsError: true}}
			}
			if blocked {
				return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: "Blocked by a preToolUse hook: " + reason, IsError: true}}
			}
			return startBackgroundTask(workDir, call)()
		}
	}

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
				// agent.Execute already caps every built-in tool's own
				// output (agent.TruncateOutput) precisely because
				// oversized content gets resent on every subsequent
				// request until /compact — an MCP result (a large gopls
				// go_search/go_package_api response, for instance) went
				// through uncapped, bypassing that entirely.
				result = agent.ToolResult{ID: call.ID, Content: agent.TruncateOutput(content), IsError: isError}
			}
		} else {
			result = agent.Execute(ctx, workDir, model, call, bash, skills, subagents, plannerModel)
		}

		if !result.IsError {
			if out, err := hooks.RunPostToolUse(ctx, workDir, hooksCfg.Hooks.PostToolUse, call.Function.Name, call.Function.Arguments, path); err != nil {
				result.Content += "\n\n(post-tool-use hook: " + err.Error() + ")"
			} else if out != "" {
				result.Content += "\n\n[hook] " + out
			}
			// Re-cap after appending hook output — result.Content was
			// already within bounds on its own, but a large postToolUse
			// hook's own output (a verbose linter, for instance) could
			// push the combined string back over the same limit.
			result.Content = agent.TruncateOutput(result.Content)
		}

		// ctx.Err() directly, not string-matching result.Content against
		// context.Canceled.Error() — the latter already broke for a
		// wrapped MCP error (see interruptibleResultText), and this is
		// the one place that still has the real ctx, not just its
		// stringified fallout. See handleToolResult's interrupted
		// handling for why this matters beyond just the displayed text:
		// esc during a tool call needs to stop the whole turn, not just
		// the one call in flight.
		return toolResultMsg{result: result, interrupted: ctx.Err() != nil}
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

// saveSession persists messages as session id for workDir. A failure
// here isn't fatal to the conversation itself, so it's reported as a
// sessionSaveErrorMsg rather than surfaced through the normal
// error-handling path.
func saveSession(workDir, id string, messages []agent.Message) tea.Cmd {
	return func() tea.Msg {
		if err := session.Save(workDir, id, messages); err != nil {
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
// compact runs any configured PreCompact hooks first (best-effort — a
// hook failing doesn't stop the compaction itself, the same
// "notification, not a gate" reasoning RunPreCompact's own doc comment
// explains) before sending messages plus the compaction instruction and
// returning the model's summary. workDir/hooksCfg are only used for
// that; a zero-value hooksCfg (no PreCompact hooks configured) skips
// the whole thing with no extra file I/O.
func compact(ctx context.Context, client *agent.Client, messages []agent.Message, focus, workDir string, hooksCfg hooks.Config) tea.Cmd {
	return func() tea.Msg {
		runPreCompactHooks(ctx, workDir, hooksCfg.Hooks.PreCompact, messages)

		prompt := compactPrompt
		if focus != "" {
			prompt += "\n\nPay particular attention to: " + focus
		}
		history := append(append([]agent.Message{}, messages...), agent.Message{Role: "user", Content: prompt})

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

// runPreCompactHooks writes messages to a temp file as markdown (reusing
// /export's own renderer) and runs every configured PreCompact hook
// against it, so a hook can back up the full conversation before it's
// replaced with a summary — see CHISEL_HOOK_TRANSCRIPT_PATH in
// hooks.RunPreCompact. Best-effort throughout: a temp-file write
// failure or a hook error is reported quietly rather than blocking the
// compaction itself, matching PreCompact's own notification-only
// contract. A no-op (no temp file even created) when there's nothing
// configured.
func runPreCompactHooks(ctx context.Context, workDir string, list []hooks.Hook, messages []agent.Message) {
	if len(list) == 0 {
		return
	}
	f, err := os.CreateTemp("", "chisel-precompact-*.md")
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(f.Name()) }()

	if _, err := f.WriteString(renderTranscriptMarkdown(messages)); err != nil {
		_ = f.Close()
		return
	}
	_ = f.Close()

	_ = hooks.RunPreCompact(ctx, workDir, list, f.Name())
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
