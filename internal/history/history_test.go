package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestLoadEmptyWhenNoFileYet(t *testing.T) {
	withHome(t)
	if got := Load(); len(got) != 0 {
		t.Errorf("Load() = %+v, want empty", got)
	}
}

func TestAppendThenLoadRoundTrip(t *testing.T) {
	withHome(t)
	if err := Append("first"); err != nil {
		t.Fatal(err)
	}
	if err := Append("second"); err != nil {
		t.Fatal(err)
	}

	got := Load()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("Load() = %+v, want [first second]", got)
	}
}

func TestAppendCapsAtMaxEntries(t *testing.T) {
	withHome(t)
	for i := 0; i < maxEntries+50; i++ {
		if err := Append(strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}

	got := Load()
	if len(got) != maxEntries {
		t.Fatalf("got %d entries, want exactly maxEntries (%d)", len(got), maxEntries)
	}
	// The oldest 50 should have been dropped — the first surviving entry
	// is "50", the last is "1049".
	if got[0] != "50" {
		t.Errorf("got[0] = %q, want %q (oldest entries dropped)", got[0], "50")
	}
	if got[len(got)-1] != strconv.Itoa(maxEntries+49) {
		t.Errorf("got[last] = %q, want %q", got[len(got)-1], strconv.Itoa(maxEntries+49))
	}
}

func TestLoadCorruptFileReturnsEmptyNotError(t *testing.T) {
	home := withHome(t)
	p := filepath.Join(home, ".chisel", "history.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := Load(); len(got) != 0 {
		t.Errorf("Load() = %+v, want empty for a corrupt file", got)
	}
}

func TestAppendWritesAtomicallyNoLeftoverTempFile(t *testing.T) {
	home := withHome(t)
	if err := Append("hello"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(home, ".chisel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after Append: %s", e.Name())
		}
	}
}

func TestAppendPreservesMultiLineEntries(t *testing.T) {
	withHome(t)
	multiline := "line one\nline two\nline three"
	if err := Append(multiline); err != nil {
		t.Fatal(err)
	}

	got := Load()
	if len(got) != 1 || got[0] != multiline {
		t.Errorf("Load() = %+v, want a single entry preserving embedded newlines", got)
	}
}

func TestHistoryFileIsValidJSON(t *testing.T) {
	home := withHome(t)
	if err := Append("hi"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".chisel", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("history.json is not valid JSON: %v", err)
	}
}
