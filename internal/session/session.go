// Package session persists chisel conversation history to disk, scoped by
// working directory, so closing and reopening chisel in the same project
// picks a conversation back up instead of starting cold every time.
// Multiple sessions can exist per directory — one file per session under
// a directory named for the workDir (see sessionDir) — rather than a
// single slot, so an earlier conversation isn't lost the moment a new
// one starts; see List/LoadByID/Resume-style callers in internal/tui.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

type file struct {
	SavedAt  time.Time       `json:"saved_at"`
	Messages []agent.Message `json:"messages"`
}

// Meta describes one saved session without needing a caller to thread
// its full message content through just to list it.
type Meta struct {
	ID           string
	Title        string
	SavedAt      time.Time
	MessageCount int
}

// idTimeFormat generates session IDs as sortable, filesystem-safe
// timestamps (no colons) — lexical sort order matches chronological
// order, and the ID doubles as the filename, so there's no separate
// manifest file to keep in sync with what's actually on disk.
const idTimeFormat = "20060102T150405.000000000Z"

// NewID generates a fresh session ID — call once when a conversation
// starts (chisel startup with nothing to resume, or /new) and reuse it
// for every subsequent Save of that same conversation, so continuing a
// session updates its own file rather than scattering a new one per turn.
func NewID() string {
	return time.Now().UTC().Format(idTimeFormat)
}

// sessionDir returns the directory holding every saved session for
// workDir — <sessionDir>/<id>.json each. Sessions are scoped per
// directory rather than kept globally — chisel is used across many
// projects, and each should resume its own conversation independently.
func sessionDir(workDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "sessions", sanitize(workDir)), nil
}

func sessionPath(workDir, id string) (string, error) {
	dir, err := sessionDir(workDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

// legacyPath is where workDir's one and only session used to live,
// before named/multiple sessions existed — a flat file, not a
// directory. Checked once by migrate so upgrading doesn't lose an
// in-progress conversation that predates this.
func legacyPath(workDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "sessions", sanitize(workDir)+".json"), nil
}

// sanitize turns an absolute path into a single filesystem-safe component,
// readable enough to eyeball in `ls ~/.chisel/sessions` — e.g.
// /home/brana/code/chisel becomes home-brana-code-chisel-3f2a91c0. The
// trailing hash (of the untouched workDir, not the readable part) is
// what actually makes the name unique — without it, distinct directories
// like /home/x/a-b and /home/x/a/b both collapse to home-x-a-b and
// would silently share (and overwrite) one session file.
func sanitize(workDir string) string {
	s := strings.TrimPrefix(filepath.ToSlash(workDir), "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		s = "root"
	}
	sum := sha256.Sum256([]byte(workDir))
	return s + "-" + hex.EncodeToString(sum[:4])
}

// migrate moves workDir's old single-file session (if any) into the new
// per-session-directory scheme, using its own saved_at as the new ID —
// best-effort: any failure here just means the legacy file is left where
// it was, tried again next call rather than losing it.
func migrate(workDir string) {
	oldPath, err := legacyPath(workDir)
	if err != nil {
		return
	}
	info, err := os.Stat(oldPath)
	if err != nil || info.IsDir() {
		return // nothing to migrate, or already migrated
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return // corrupt legacy file — leave it; nothing usable to migrate
	}

	id := f.SavedAt.UTC().Format(idTimeFormat)
	if f.SavedAt.IsZero() {
		id = NewID()
	}
	path, err := sessionPath(workDir, id)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if writeAtomic(path, data) != nil {
		return
	}
	_ = os.Remove(oldPath)
}

// List returns every saved session for workDir, most recent first.
// Migrates a pre-existing single-file session transparently first (see
// migrate) so it shows up here too. Includes sessions with zero messages
// — /new and /resume now save immediately rather than waiting for a
// turn to complete (see tui.handleNewCommand), specifically so quitting
// right after either one still resumes the right session next launch;
// excluding empty ones here would silently undo that.
func List(workDir string) ([]Meta, error) {
	migrate(workDir)

	dir, err := sessionDir(workDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f file
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		metas = append(metas, Meta{
			ID:           strings.TrimSuffix(e.Name(), ".json"),
			Title:        deriveTitle(f.Messages),
			SavedAt:      f.SavedAt,
			MessageCount: len(f.Messages),
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].SavedAt.After(metas[j].SavedAt) })
	return metas, nil
}

// deriveTitle finds the first user message in messages and truncates it
// to a short, single-line label for display in /sessions.
func deriveTitle(messages []agent.Message) string {
	for _, msg := range messages {
		if msg.Role != "user" || msg.Content == "" {
			continue
		}
		line := msg.Content
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		const maxLen = 60
		if runes := []rune(line); len(runes) > maxLen {
			line = string(runes[:maxLen]) + "…"
		}
		return line
	}
	return "(no messages yet)"
}

// LoadByID returns a specific saved session by ID — ok is false if it
// doesn't exist or couldn't be read/parsed. A zero-message session
// (see List's own doc comment on why those exist now) is still ok=true:
// it's a real, just-started session, not an absent one.
func LoadByID(workDir, id string) (messages []agent.Message, savedAt time.Time, ok bool) {
	path, err := sessionPath(workDir, id)
	if err != nil {
		return nil, time.Time{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, time.Time{}, false
	}
	return f.Messages, f.SavedAt, true
}

// LoadLatest returns the most recently saved session for workDir — what
// a plain chisel launch auto-resumes, preserving the single-session
// behavior from before named/multiple sessions existed. id is the
// resumed session's own ID, for saving back to the same file rather than
// starting a new one on the very next turn; empty if ok is false. corrupt
// distinguishes "a session existed but couldn't actually be loaded" from
// "there's simply nothing to resume yet" (the overwhelmingly common case
// for a fresh directory), the same distinction the old single-session
// Load already made — see its removed doc comment for why that mattered.
//
// Whenever corrupt is true, the offending file is renamed to
// "<id>.json.corrupt" (best-effort) before returning — otherwise the
// same corrupt file triggered the same warning on every single future
// startup forever, with no way to make it stop short of deleting it by
// hand. The renamed file is left on disk (not deleted outright, in case
// its content is worth recovering) but no longer matches the ".json"
// suffix List/LoadByID look for, so it won't be found — and won't
// trigger the warning — again; PruneOld still sweeps it up eventually
// (see isSessionFile).
func LoadLatest(workDir string) (messages []agent.Message, savedAt time.Time, id string, ok bool, corrupt bool) {
	metas, err := List(workDir)
	if err != nil {
		return nil, time.Time{}, "", false, false
	}
	if len(metas) > 0 {
		latest := metas[0]
		msgs, sAt, ok := LoadByID(workDir, latest.ID)
		if !ok {
			if path, pathErr := sessionPath(workDir, latest.ID); pathErr == nil {
				_ = os.Rename(path, path+".corrupt")
			}
			return nil, time.Time{}, "", false, true
		}
		return msgs, sAt, latest.ID, true, false
	}

	// List found nothing usable — distinguish a directory that has a
	// .json file List() simply couldn't parse (corrupt, worth a warning)
	// from there genuinely being no session directory, or an empty one,
	// at all (the overwhelmingly common case for a fresh working
	// directory, not worth one).
	dir, err := sessionDir(workDir)
	if err != nil {
		return nil, time.Time{}, "", false, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, time.Time{}, "", false, false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			path := filepath.Join(dir, e.Name())
			_ = os.Rename(path, path+".corrupt")
			return nil, time.Time{}, "", false, true
		}
	}
	return nil, time.Time{}, "", false, false
}

// staleSessionAge bounds how long an unresumed session file is kept
// before PruneOld treats it as abandoned and removes it. There's no way
// to tell whether the project directory a session belongs to still
// exists — sanitize (see its own doc comment) is one-way, so the
// original workDir can't be recovered from the directory name alone —
// so this prunes by age instead: a session nobody has resumed in three
// months is overwhelmingly likely to belong to a project that's gone or
// dormant, or is simply done with.
const staleSessionAge = 90 * 24 * time.Hour

// PruneOld removes individual session files (across every working
// directory chisel has ever been run in) whose own recorded save time is
// older than staleSessionAge — without this, every session ever saved,
// in every project, sticks around forever. Prunes per-file within each
// directory's session folder, not the whole folder at once, so a
// directory with some recent and some abandoned sessions only loses the
// abandoned ones. Best-effort throughout: a missing sessions root isn't
// an error, and one file that can't be read, parsed, or removed doesn't
// stop the rest from being checked. Callers should run this after
// resuming whatever session they actually wanted (see main.go) — the
// resumed messages are already in memory by then regardless of what
// happens to the file next, so there's no risk of pruning out from
// under a session still being loaded.
func PruneOld() (removed int, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	root := filepath.Join(home, ".chisel", "sessions")
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := time.Now().Add(-staleSessionAge)
	for _, re := range rootEntries {
		full := filepath.Join(root, re.Name())
		if !re.IsDir() {
			// A legacy flat file never migrated (its directory was never
			// resumed since the upgrade) — the same age check still
			// applies directly to it.
			if isSessionFile(re.Name()) && pruneFileIfStale(full, cutoff) {
				removed++
			}
			continue
		}
		sessionFiles, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, sf := range sessionFiles {
			if sf.IsDir() || !isSessionFile(sf.Name()) {
				continue
			}
			if pruneFileIfStale(filepath.Join(full, sf.Name()), cutoff) {
				removed++
			}
		}
	}
	return removed, nil
}

// isSessionFile reports whether name is something PruneOld should
// consider — a normal session file, or one LoadLatest renamed after
// finding it corrupt (see the ".corrupt" rename there): left in place
// for manual inspection rather than deleted outright, but still worth
// eventually sweeping up by the same age rule rather than accumulating
// forever.
func isSessionFile(name string) bool {
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json.corrupt")
}

// pruneFileIfStale removes path if it's older than cutoff. Checks mtime
// first via Stat, not a full ReadFile+unmarshal — SavedAt (baked into
// content) tracks mtime within seconds, since every Save rewrites the
// file atomically, so a file whose mtime alone is already newer than
// cutoff can't possibly be stale and there's no need to read its content
// at all. PruneOld runs at every startup across every session ever
// saved, in every project; with 90-day retention, the overwhelming
// majority of files are nowhere near stale, so skipping the read for all
// of them is the common case, not an edge one. Only once mtime looks old
// enough to prune does this bother reading content, for the
// authoritative SavedAt rather than trusting mtime as gospel for the
// actual delete decision — falling back to mtime again if the content
// turns out to be corrupt/unparseable (a corrupt file that never aged
// out on its own was the original reason this fallback existed).
func pruneFileIfStale(path string, cutoff time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.ModTime().After(cutoff) {
		return false
	}
	savedAt, ok := savedAtFromContent(path)
	if !ok {
		savedAt = info.ModTime()
	}
	return savedAt.Before(cutoff) && os.Remove(path) == nil
}

// savedAtFromContent reads and parses path for its own recorded
// SavedAt — ok is false if the file can't be read, doesn't parse, or
// has a zero SavedAt (the corrupt-file case pruneFileIfStale falls back
// from).
func savedAtFromContent(path string) (time.Time, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil || f.SavedAt.IsZero() {
		return time.Time{}, false
	}
	return f.SavedAt, true
}

// Save writes messages as session id for workDir, creating its session
// directory if needed. id is generated once per conversation (see
// NewID) and reused on every subsequent save of it, so continuing a
// session updates its own file instead of scattering one per turn.
func Save(workDir, id string, messages []agent.Message) error {
	path, err := sessionPath(workDir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.Marshal(file{SavedAt: time.Now(), Messages: messages})
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeAtomic writes data to path via a temp file plus rename, rather
// than in place — an in-place write truncates the existing file first,
// so a crash (or any interruption) partway through would otherwise leave
// a truncated, unparseable session behind. Rename is atomic on the same
// filesystem — path always ends up as either the old content or the
// complete new content, never a partial write.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".session-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
