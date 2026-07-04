package session

import (
	"os"
	"path/filepath"
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
		"/home/brana/code/chisel": "home-brana-code-chisel",
		"/":                       "root",
		"/a/b/c":                  "a-b-c",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
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
