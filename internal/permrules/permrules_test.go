package permrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmarshalPreservesRuleOrder(t *testing.T) {
	var cfg Config
	data := []byte(`{"bash":{"git *":"allow","git push --force*":"deny","*":"deny"}}`)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rules := cfg["bash"]
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rules))
	}
	want := []Rule{
		{Pattern: "git *", Decision: Allow},
		{Pattern: "git push --force*", Decision: Deny},
		{Pattern: "*", Decision: Deny},
	}
	for i, w := range want {
		if rules[i] != w {
			t.Errorf("rules[%d] = %+v, want %+v", i, rules[i], w)
		}
	}
}

func TestUnmarshalRejectsInvalidDecision(t *testing.T) {
	var cfg Config
	err := json.Unmarshal([]byte(`{"bash":{"git *":"maybe"}}`), &cfg)
	if err == nil {
		t.Error("expected an error for an invalid decision value")
	}
}

// TestGlobMatchCrossesSlashes is the reason globMatch doesn't use
// path.Match/filepath.Match: those treat "/" as a path separator "*"
// won't cross, which breaks a pattern like "git *" against a command
// containing a file path, branch name, or URL — all routine in real
// shell commands.
func TestGlobMatchCrossesSlashes(t *testing.T) {
	cases := []struct {
		pattern, text string
		want          bool
	}{
		{"git *", "git checkout feature/foo", true},
		{"git *", "git push origin main", true},
		{"npm run *", "npm run build", true},
		{"curl * https://*", "curl -sL https://example.com/install.sh", true},
		{"rm -rf *", "rm -rf /", true},
		{"git *", "npm install", false},
		{"git push*", "git pull", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.text); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.text, got, c.want)
		}
	}
}

func TestGlobMatchQuestionMark(t *testing.T) {
	if !globMatch("go test ./pkg?", "go test ./pkg1") {
		t.Error("expected ? to match exactly one character")
	}
	if globMatch("go test ./pkg?", "go test ./pkg12") {
		t.Error("expected ? to not match two characters")
	}
}

func TestGlobMatchEscapesRegexMetacharacters(t *testing.T) {
	if !globMatch("go build ./...", "go build ./...") {
		t.Error("expected a literal '.' in the pattern to match literally, not as a regex wildcard")
	}
	if globMatch("go build ./...", "go build ./xxx") {
		t.Error("'.' in the pattern should not have matched an arbitrary character")
	}
}

func TestMatchLastRuleWins(t *testing.T) {
	cfg := Config{
		"bash": RuleList{
			{Pattern: "git *", Decision: Allow},
			{Pattern: "git push --force*", Decision: Deny},
		},
	}

	decision, matched := Match(cfg, "bash", "git push --force origin main")
	if !matched || decision != Deny {
		t.Errorf("decision = %v, matched = %v, want Deny/true — the more specific later rule should win", decision, matched)
	}

	decision, matched = Match(cfg, "bash", "git status")
	if !matched || decision != Allow {
		t.Errorf("decision = %v, matched = %v, want Allow/true", decision, matched)
	}
}

func TestMatchNoRuleForTool(t *testing.T) {
	cfg := Config{"bash": RuleList{{Pattern: "*", Decision: Allow}}}
	_, matched := Match(cfg, "str_replace_based_edit_tool", "anything")
	if matched {
		t.Error("expected no match for a tool with no configured rules")
	}
}

func TestMatchNoPatternMatches(t *testing.T) {
	cfg := Config{"bash": RuleList{{Pattern: "git *", Decision: Allow}}}
	_, matched := Match(cfg, "bash", "npm install")
	if matched {
		t.Error("expected no match when no pattern matches the text")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	cfg, found, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if found {
		t.Error("found = true, want false for a missing permissions.json")
	}
	if cfg.HasAny() {
		t.Error("expected an empty config")
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".chisel"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"bash":{"git *":"allow","rm -rf *":"deny"}}`
	if err := os.WriteFile(ConfigPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, found, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if !cfg.HasAny() {
		t.Error("expected a non-empty config")
	}
	decision, matched := Match(cfg, "bash", "rm -rf /tmp/x")
	if !matched || decision != Deny {
		t.Errorf("decision = %v, matched = %v, want Deny/true", decision, matched)
	}
}

func TestLoadMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".chisel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(dir), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, found, err := Load(dir)
	if err == nil {
		t.Error("expected an error for malformed JSON")
	}
	if !found {
		t.Error("found = false, want true — the file did exist, it just failed to parse")
	}
}

func TestHasAnyEmptyConfig(t *testing.T) {
	var cfg Config
	if cfg.HasAny() {
		t.Error("HasAny() = true for a nil config")
	}
}

func TestMarshalJSONRoundTripsThroughUnmarshal(t *testing.T) {
	cfg := Config{
		"bash": RuleList{
			{Pattern: "git *", Decision: Allow},
			{Pattern: "git push --force*", Decision: Deny},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(%s): %v", data, err)
	}
	if len(got["bash"]) != 2 || got["bash"][0] != cfg["bash"][0] || got["bash"][1] != cfg["bash"][1] {
		t.Errorf("round-tripped config = %+v, want %+v", got, cfg)
	}
}

func TestAddAppendsRuleAndPreservesOrder(t *testing.T) {
	cfg := Add(nil, "bash", "git *", Allow)
	cfg = Add(cfg, "bash", "git push --force*", Deny)

	rules := cfg["bash"]
	want := []Rule{
		{Pattern: "git *", Decision: Allow},
		{Pattern: "git push --force*", Decision: Deny},
	}
	if len(rules) != len(want) {
		t.Fatalf("got %d rules, want %d", len(rules), len(want))
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rules[%d] = %+v, want %+v", i, rules[i], want[i])
		}
	}

	// The later, more specific rule must still win via last-match-wins.
	if decision, _ := Match(cfg, "bash", "git push --force origin main"); decision != Deny {
		t.Errorf("Match = %v, want Deny (the more specific, later rule)", decision)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	workDir := t.TempDir()
	cfg := Add(nil, "bash", "npm test*", Allow)

	if err := Save(workDir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, found, err := Load(workDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("Load: found = false after Save")
	}
	if decision, matched := Match(loaded, "bash", "npm test --watch"); !matched || decision != Allow {
		t.Errorf("Match after Save+Load = (%v, %v), want (Allow, true)", decision, matched)
	}
}

// TestSaveWritesAtomicallyNoLeftoverTempFile is the regression test for
// a real robustness gap: Save wrote its file via a plain os.WriteFile,
// unlike internal/session's Save and internal/trust's Trust (both
// temp-file + rename) — a crash mid-write could leave a truncated,
// corrupt permissions.json behind, silently dropping the user's
// curated allow/deny policy on the next launch.
func TestSaveWritesAtomicallyNoLeftoverTempFile(t *testing.T) {
	workDir := t.TempDir()
	cfg := Add(nil, "bash", "npm test*", Allow)

	if err := Save(workDir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Dir(ConfigPath(workDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}
	if _, err := os.Stat(ConfigPath(workDir)); err != nil {
		t.Errorf("expected the real permissions file to exist: %v", err)
	}
}

func TestTrustRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hash := ContentHash([]byte(`{"bash":{"git *":"allow"}}`))

	trusted, err := IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("should not be trusted before Trust is called")
	}

	if err := Trust(hash); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	trusted, err = IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted after Trust: %v", err)
	}
	if !trusted {
		t.Error("expected the hash to be trusted after Trust was called")
	}
}
