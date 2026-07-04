package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

// withHome points os.UserHomeDir (via HOME) at a temp dir for the test.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"/home/brana/code/chisel": "home-brana-code-chisel-",
		"/":                       "root-",
		"/a/b/c":                  "a-b-c-",
	}
	for in, wantPrefix := range cases {
		if got := sanitize(in); !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("sanitize(%q) = %q, want it to start with %q", in, got, wantPrefix)
		}
	}
}

func TestSanitizeIsDeterministic(t *testing.T) {
	const path = "/home/brana/code/chisel"
	first := sanitize(path)
	second := sanitize(path)
	if first != second {
		t.Errorf("sanitize(%q) = %q then %q, want the same value both times", path, first, second)
	}
}

// TestSanitizeAvoidsCollisionsFromDifferentDirectoryStructures is the
// regression test for a real bug: readable-only sanitization collapsed
// distinct directories like /home/x/a-b and /home/x/a/b to the exact
// same string ("home-x-a-b"), so resuming a session in one would
// silently load (and overwrite) the other's conversation.
func TestSanitizeAvoidsCollisionsFromDifferentDirectoryStructures(t *testing.T) {
	a := sanitize("/home/x/a-b")
	b := sanitize("/home/x/a/b")
	if a == b {
		t.Errorf("sanitize(%q) and sanitize(%q) collided: both %q", "/home/x/a-b", "/home/x/a/b", a)
	}
}

func TestSaveAndLoadLatestRoundTrip(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"
	id := NewID()

	messages := []agent.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	if err := Save(workDir, id, messages); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, savedAt, gotID, ok, _ := LoadLatest(workDir)
	if !ok {
		t.Fatal("LoadLatest: ok = false, want true")
	}
	if gotID != id {
		t.Errorf("LoadLatest id = %q, want %q", gotID, id)
	}
	if savedAt.IsZero() {
		t.Error("savedAt is zero, want a real timestamp")
	}
	if len(got) != 2 || got[0].Content != "hello" || got[1].Content != "hi there" {
		t.Errorf("got = %+v", got)
	}
}

func TestLoadLatestNoSavedSession(t *testing.T) {
	withHome(t)
	_, _, id, ok, corrupt := LoadLatest("/nonexistent/work/dir")
	if ok {
		t.Error("LoadLatest: ok = true for a directory with no saved session")
	}
	if id != "" {
		t.Errorf("LoadLatest: id = %q, want empty", id)
	}
	if corrupt {
		t.Error("LoadLatest: corrupt = true for a directory that simply has no saved session yet — want false, this isn't a warning-worthy case")
	}
}

func TestLoadLatestReturnsMostRecentOfSeveral(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	if err := Save(workDir, "20260101T000000.000000000Z", []agent.Message{{Role: "user", Content: "older"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(workDir, "20260601T000000.000000000Z", []agent.Message{{Role: "user", Content: "newer"}}); err != nil {
		t.Fatal(err)
	}

	got, _, id, ok, _ := LoadLatest(workDir)
	if !ok || len(got) != 1 || got[0].Content != "newer" {
		t.Errorf("LoadLatest = %+v (id=%q, ok=%v), want the newer session", got, id, ok)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	path, err := sessionPath(workDir, "some-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, id, ok, corrupt := LoadLatest(workDir)
	if ok {
		t.Error("LoadLatest: ok = true for a corrupt session file, want a clean false")
	}
	if id != "" {
		t.Errorf("LoadLatest: id = %q, want empty", id)
	}
	if !corrupt {
		t.Error("LoadLatest: corrupt = false for a session file that exists but failed to parse, want true")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected the corrupt file to be renamed away from its original .json path")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("expected the corrupt content preserved at %s.corrupt: %v", path, err)
	}
}

// TestLoadLatestCorruptFileOnlyWarnsOnce is the regression test for a
// real UX bug: a corrupt session file used to trigger the "couldn't be
// read" warning on every single startup, forever, with no way to make
// it stop short of deleting the file by hand. Renaming it out of the
// way (see LoadLatest's own doc comment) means the *next* call sees a
// clean, resumable-nothing directory instead of the same corruption again.
func TestLoadLatestCorruptFileOnlyWarnsOnce(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	path, err := sessionPath(workDir, "some-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, _, ok1, corrupt1 := LoadLatest(workDir)
	if ok1 || !corrupt1 {
		t.Fatalf("first LoadLatest: ok=%v corrupt=%v, want false/true", ok1, corrupt1)
	}

	_, _, _, ok2, corrupt2 := LoadLatest(workDir)
	if ok2 {
		t.Error("second LoadLatest: ok = true, want still nothing to resume")
	}
	if corrupt2 {
		t.Error("second LoadLatest: corrupt = true again — expected the rename to stop the repeat warning")
	}
}

// TestPruneOldEventuallyRemovesRenamedCorruptFiles confirms a corrupt
// file renamed by LoadLatest doesn't just sit under ~/.chisel forever —
// it's still swept up by the normal age-based pruning eventually,
// falling back to the file's own mtime since its content can't be
// parsed for a SavedAt.
func TestPruneOldEventuallyRemovesRenamedCorruptFiles(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	path, err := sessionPath(workDir, "some-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	corruptPath := path + ".corrupt"
	if err := os.WriteFile(corruptPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleSessionAge - 24*time.Hour)
	if err := os.Chtimes(corruptPath, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOld()
	if err != nil {
		t.Fatalf("PruneOld: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Error("expected the old renamed-corrupt file to be pruned")
	}
}

func TestListReturnsMostRecentFirst(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	if err := Save(workDir, "20260101T000000.000000000Z", []agent.Message{{Role: "user", Content: "first session"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(workDir, "20260601T000000.000000000Z", []agent.Message{{Role: "user", Content: "second session"}}); err != nil {
		t.Fatal(err)
	}

	metas, err := List(workDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d sessions, want 2", len(metas))
	}
	if metas[0].Title != "second session" || metas[1].Title != "first session" {
		t.Errorf("metas = %+v, want most-recent-first ordering", metas)
	}
	if metas[0].MessageCount != 1 {
		t.Errorf("metas[0].MessageCount = %d, want 1", metas[0].MessageCount)
	}
}

func TestListEmptyForDirectoryWithNoSessions(t *testing.T) {
	withHome(t)
	metas, err := List("/nonexistent/work/dir")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("metas = %+v, want empty", metas)
	}
}

// TestListIncludesEmptySessions and TestLoadLatestResumesEmptySession
// are the regression tests for active-session tracking: /new and
// /resume now save immediately (even with zero messages) so quitting
// right after either one still resumes the right session next launch —
// which only works if an empty session isn't silently treated as if it
// doesn't exist.
func TestListIncludesEmptySessions(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"
	id := NewID()
	if err := Save(workDir, id, nil); err != nil {
		t.Fatal(err)
	}

	metas, err := List(workDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != id || metas[0].MessageCount != 0 {
		t.Errorf("metas = %+v, want a single empty session with id %q", metas, id)
	}
}

func TestLoadLatestResumesEmptySession(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"
	id := NewID()
	if err := Save(workDir, id, nil); err != nil {
		t.Fatal(err)
	}

	messages, _, gotID, ok, corrupt := LoadLatest(workDir)
	if !ok {
		t.Fatal("LoadLatest: ok = false for a freshly-saved empty session")
	}
	if corrupt {
		t.Error("LoadLatest: corrupt = true for a legitimately empty session")
	}
	if gotID != id {
		t.Errorf("LoadLatest id = %q, want %q", gotID, id)
	}
	if len(messages) != 0 {
		t.Errorf("messages = %+v, want empty", messages)
	}
}

func TestLoadByIDEmptySessionIsOK(t *testing.T) {
	workDir := t.TempDir()
	id := NewID()
	if err := Save(workDir, id, nil); err != nil {
		t.Fatal(err)
	}

	messages, _, ok := LoadByID(workDir, id)
	if !ok {
		t.Error("LoadByID: ok = false for a legitimately empty session")
	}
	if len(messages) != 0 {
		t.Errorf("messages = %+v, want empty", messages)
	}
}

func TestLoadByIDMissingSessionIsNotOK(t *testing.T) {
	withHome(t)
	_, _, ok := LoadByID("/home/brana/code/testproj", "does-not-exist")
	if ok {
		t.Error("LoadByID: ok = true for a nonexistent session id")
	}
}

func TestDeriveTitleTruncatesLongFirstMessage(t *testing.T) {
	messages := []agent.Message{{Role: "user", Content: strings.Repeat("x", 100)}}
	title := deriveTitle(messages)
	if len([]rune(title)) > 61 { // 60 + the ellipsis rune
		t.Errorf("title = %q (%d runes), want truncated to ~60", title, len([]rune(title)))
	}
}

func TestDeriveTitleFallsBackWhenNoUserMessage(t *testing.T) {
	messages := []agent.Message{{Role: "assistant", Content: "hi"}}
	if got := deriveTitle(messages); got != "(no messages yet)" {
		t.Errorf("deriveTitle = %q, want the fallback text", got)
	}
}

// TestMigrateOldSingleFileFormat is the regression test for the
// named/multiple-sessions upgrade: a user with an existing single-file
// session (the format before this) must not lose it — it should show up
// in List/LoadLatest exactly as if it had always been a normal saved
// session, and the old flat file should be gone afterward.
func TestMigrateOldSingleFileFormat(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	oldPath, err := legacyPath(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	oldData, err := json.Marshal(file{
		SavedAt:  time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Messages: []agent.Message{{Role: "user", Content: "a conversation from before multi-session support"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, oldData, 0o600); err != nil {
		t.Fatal(err)
	}

	messages, _, id, ok, corrupt := LoadLatest(workDir)
	if !ok {
		t.Fatal("LoadLatest: ok = false, want the migrated legacy session to load")
	}
	if corrupt {
		t.Error("LoadLatest: corrupt = true, want a clean migration")
	}
	if id == "" {
		t.Error("LoadLatest: id is empty, want a real migrated session id")
	}
	if len(messages) != 1 || messages[0].Content != "a conversation from before multi-session support" {
		t.Errorf("messages = %+v", messages)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("expected the old flat-file session to be removed after migration")
	}

	// Confirm it's really in the new per-session-directory form.
	dir, err := sessionDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d files in the new session directory, want exactly 1", len(entries))
	}
}

// TestPruneFileIfStaleSkipsReadWhenMtimeIsFresh documents the
// performance fast path pruneFileIfStale takes: PruneOld runs at every
// startup across every session ever saved, in every project, and with
// 90-day retention the overwhelming majority of files are nowhere near
// stale — a file's own mtime (checked via Stat, no content read) is
// enough to rule that out immediately, since SavedAt is baked into
// content at the same moment Save writes the file, always in sync in
// real usage. A file with a stale SavedAt in content but a fresh mtime
// (only reachable by tampering, not by any real Save) is *not* pruned —
// mtime freshness short-circuits before content is ever read.
// TestResavingResumedSessionSurvivesImmediatePrune is the regression
// test for a real data-loss bug in main.go's startup sequence: it calls
// LoadLatest then PruneOld back to back on every launch. If the latest
// session for this directory happens to be older than staleSessionAge
// (returning to a long-dormant project), LoadLatest resumes it and the
// very next PruneOld call used to delete its file immediately —
// quitting before the first turn completed lost the conversation for
// good, even though it had just been shown as resumed. main.go now
// re-saves the resumed session (bumping SavedAt) between those two
// calls, exactly what this test proves is sufficient.
func TestResavingResumedSessionSurvivesImmediatePrune(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/dormant-project"

	id := NewID()
	dormantMessages := []agent.Message{{Role: "user", Content: "haven't touched this in months"}}
	if err := Save(workDir, id, dormantMessages); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-staleSessionAge - 24*time.Hour)
	path, err := sessionPath(workDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	// Also backdate the content's own SavedAt to match — otherwise
	// LoadLatest's own re-save (mirroring main.go) would already bump it
	// before PruneOld ever got a chance to look at it, which wouldn't
	// actually exercise the race this test is about.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f.SavedAt = oldTime
	data, err = json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Mirrors main.go's exact sequence: LoadLatest, then re-save, then PruneOld.
	messages, _, gotID, ok, corrupt := LoadLatest(workDir)
	if !ok || corrupt {
		t.Fatalf("LoadLatest: ok=%v corrupt=%v, want true/false", ok, corrupt)
	}
	if err := Save(workDir, gotID, messages); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if _, err := PruneOld(); err != nil {
		t.Fatalf("PruneOld: %v", err)
	}

	if _, _, ok := LoadByID(workDir, gotID); !ok {
		t.Error("expected the resumed session to survive the immediately-following PruneOld")
	}
}

func TestPruneFileIfStaleSkipsReadWhenMtimeIsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	old := file{SavedAt: time.Now().Add(-staleSessionAge - 24*time.Hour), Messages: []agent.Message{{Role: "user", Content: "x"}}}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave the file's mtime at "now" (os.WriteFile's
	// default) despite the old SavedAt inside — the mismatch this test
	// is about.

	if pruneFileIfStale(path, time.Now().Add(-staleSessionAge)) {
		t.Error("expected the fresh-mtime file to survive despite a stale SavedAt in its content")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("expected the file to still exist")
	}
}

func TestPruneOldRemovesStaleSessionsOnly(t *testing.T) {
	withHome(t)

	// A fresh, recently-saved session.
	if err := Save("/home/brana/code/fresh", NewID(), []agent.Message{{Role: "user", Content: "recent"}}); err != nil {
		t.Fatal(err)
	}

	// A stale session — write it directly with an old saved_at, since
	// Save always stamps time.Now().
	staleID := "stale-id"
	stalePath, err := sessionPath("/home/brana/code/stale", staleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-staleSessionAge - 24*time.Hour)
	old := file{SavedAt: oldTime, Messages: []agent.Message{{Role: "user", Content: "ancient"}}}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// pruneFileIfStale checks mtime before bothering to parse content
	// (see its own doc comment) — match it to SavedAt the way a real
	// Save always does, since a real stale file would have both, not
	// just an old SavedAt with a fresh mtime from this test's setup.
	if err := os.Chtimes(stalePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOld()
	if err != nil {
		t.Fatalf("PruneOld: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want exactly 1 (the stale session)", removed)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("expected the stale session file to be removed")
	}
	if _, _, _, ok, _ := LoadLatest("/home/brana/code/fresh"); !ok {
		t.Error("expected the fresh session to survive pruning")
	}
}

func TestPruneOldKeepsFreshSessionsInAMixedDirectory(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/mixed"

	freshID := NewID()
	if err := Save(workDir, freshID, []agent.Message{{Role: "user", Content: "fresh"}}); err != nil {
		t.Fatal(err)
	}
	stalePath, err := sessionPath(workDir, "stale-id")
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-staleSessionAge - 24*time.Hour)
	old := file{SavedAt: oldTime, Messages: []agent.Message{{Role: "user", Content: "ancient"}}}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(stalePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stalePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOld()
	if err != nil {
		t.Fatalf("PruneOld: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	metas, err := List(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != freshID {
		t.Errorf("metas = %+v, want only the fresh session (%q) to survive", metas, freshID)
	}
}

func TestPruneOldNoSessionsDirIsNotAnError(t *testing.T) {
	withHome(t)
	removed, err := PruneOld()
	if err != nil {
		t.Fatalf("PruneOld with no sessions dir yet: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

func TestSaveWritesAtomicallyNoLeftoverTempFile(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"
	id := NewID()

	if err := Save(workDir, id, []agent.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir, err := sessionDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}

	path, err := sessionPath(workDir, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

func TestSaveOverwritesSameIDCompletely(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"
	id := NewID()

	long := []agent.Message{{Role: "user", Content: strings.Repeat("x", 10_000)}}
	if err := Save(workDir, id, long); err != nil {
		t.Fatal(err)
	}

	short := []agent.Message{{Role: "user", Content: "short"}}
	if err := Save(workDir, id, short); err != nil {
		t.Fatal(err)
	}

	got, _, ok := LoadByID(workDir, id)
	if !ok || len(got) != 1 || got[0].Content != "short" {
		t.Errorf("got = %+v, ok=%v, want just the short overwrite with no trailing garbage from the longer previous save", got, ok)
	}
}

func TestSaveWithDifferentIDsCreatesSeparateSessions(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	id1 := NewID()
	if err := Save(workDir, id1, []agent.Message{{Role: "user", Content: "first"}}); err != nil {
		t.Fatal(err)
	}
	id2 := "20260601T000000.000000000Z"
	if err := Save(workDir, id2, []agent.Message{{Role: "user", Content: "second"}}); err != nil {
		t.Fatal(err)
	}

	got1, _, ok := LoadByID(workDir, id1)
	if !ok || got1[0].Content != "first" {
		t.Errorf("session %q = %+v", id1, got1)
	}
	got2, _, ok := LoadByID(workDir, id2)
	if !ok || got2[0].Content != "second" {
		t.Errorf("session %q = %+v", id2, got2)
	}

	metas, err := List(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Errorf("got %d sessions, want 2 — /new starting a new session must not delete the previous one", len(metas))
	}
}

func TestDifferentWorkDirsAreIndependent(t *testing.T) {
	withHome(t)

	if err := Save("/home/brana/code/proj-a", NewID(), []agent.Message{{Role: "user", Content: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save("/home/brana/code/proj-b", NewID(), []agent.Message{{Role: "user", Content: "b"}}); err != nil {
		t.Fatal(err)
	}

	a, _, _, ok, _ := LoadLatest("/home/brana/code/proj-a")
	if !ok || a[0].Content != "a" {
		t.Errorf("proj-a session = %+v, ok=%v", a, ok)
	}
	b, _, _, ok, _ := LoadLatest("/home/brana/code/proj-b")
	if !ok || b[0].Content != "b" {
		t.Errorf("proj-b session = %+v, ok=%v", b, ok)
	}
}
