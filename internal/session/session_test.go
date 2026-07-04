package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestSaveAndLoadRoundTrip(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	messages := []agent.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	if err := Save(workDir, messages); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, savedAt, ok := Load(workDir)
	if !ok {
		t.Fatal("Load: ok = false, want true")
	}
	if savedAt.IsZero() {
		t.Error("savedAt is zero, want a real timestamp")
	}
	if len(got) != 2 || got[0].Content != "hello" || got[1].Content != "hi there" {
		t.Errorf("got = %+v", got)
	}
}

func TestLoadNoSavedSession(t *testing.T) {
	withHome(t)
	_, _, ok := Load("/nonexistent/work/dir")
	if ok {
		t.Error("Load: ok = true for a directory with no saved session")
	}
}

func TestLoadCorruptFile(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	path, err := Path(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, ok := Load(workDir)
	if ok {
		t.Error("Load: ok = true for a corrupt session file, want a clean false")
	}
}

func TestClear(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	if err := Save(workDir, []agent.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if err := Clear(workDir); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if _, _, ok := Load(workDir); ok {
		t.Error("Load after Clear: ok = true, want false")
	}

	// Clearing again (nothing left to remove) must not be an error.
	if err := Clear(workDir); err != nil {
		t.Errorf("Clear on an already-cleared session: %v", err)
	}
}

func TestSaveWritesAtomicallyNoLeftoverTempFile(t *testing.T) {
	home := withHome(t)
	workDir := "/home/brana/code/testproj"

	if err := Save(workDir, []agent.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(home, ".chisel", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}

	path, err := Path(workDir)
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

func TestSaveOverwritesPreviousContentCompletely(t *testing.T) {
	withHome(t)
	workDir := "/home/brana/code/testproj"

	long := []agent.Message{{Role: "user", Content: strings.Repeat("x", 10_000)}}
	if err := Save(workDir, long); err != nil {
		t.Fatal(err)
	}

	short := []agent.Message{{Role: "user", Content: "short"}}
	if err := Save(workDir, short); err != nil {
		t.Fatal(err)
	}

	got, _, ok := Load(workDir)
	if !ok || len(got) != 1 || got[0].Content != "short" {
		t.Errorf("got = %+v, ok=%v, want just the short overwrite with no trailing garbage from the longer previous save", got, ok)
	}
}

func TestDifferentWorkDirsAreIndependent(t *testing.T) {
	withHome(t)

	if err := Save("/home/brana/code/proj-a", []agent.Message{{Role: "user", Content: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save("/home/brana/code/proj-b", []agent.Message{{Role: "user", Content: "b"}}); err != nil {
		t.Fatal(err)
	}

	a, _, ok := Load("/home/brana/code/proj-a")
	if !ok || a[0].Content != "a" {
		t.Errorf("proj-a session = %+v, ok=%v", a, ok)
	}
	b, _, ok := Load("/home/brana/code/proj-b")
	if !ok || b[0].Content != "b" {
		t.Errorf("proj-b session = %+v, ok=%v", b, ok)
	}
}
