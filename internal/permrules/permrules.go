// Package permrules loads user-defined, persistent permission rules —
// glob patterns per tool that pre-decide whether a call needs
// confirmation, surviving across sessions unlike the in-memory
// "always allow this session" mechanism internal/tui's permission.go
// already has. Project-scoped (<workDir>/.chisel/permissions.json),
// trust-gated like hooks: a rule that allows a dangerous call can
// silently bypass confirmation the same way a hook can silently
// execute arbitrary code, so loading one needs the same one-time
// approval — see ContentHash/IsTrusted/Trust, backed by
// internal/trust, the same mechanism internal/hooks uses.
package permrules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BikeshR/chisel/internal/trust"
)

// Decision is what a rule says about a call whose pattern it matches.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
)

// Rule is one pattern-to-decision pair.
type Rule struct {
	Pattern  string
	Decision Decision
}

// RuleList is a tool's ordered rules — order matters: Match applies
// last-match-wins, the same convention OpenCode's own permission rules
// use, so a more specific later rule can override an earlier, broader
// one (e.g. "git *": allow, then "git push --force*": deny). JSON
// object keys don't preserve order through encoding/json's normal
// map-based decoding, so RuleList implements UnmarshalJSON itself,
// reading the object's keys in the order they appear via json.Decoder's
// token stream instead.
type RuleList []Rule

func (r *RuleList) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil {
		return err
	} else if tok != json.Delim('{') {
		return fmt.Errorf("expected a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		pattern, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected a string key, got %v", keyTok)
		}
		var decision string
		if err := dec.Decode(&decision); err != nil {
			return err
		}
		if decision != string(Allow) && decision != string(Deny) {
			return fmt.Errorf("invalid decision %q for pattern %q — must be %q or %q", decision, pattern, Allow, Deny)
		}
		*r = append(*r, Rule{Pattern: pattern, Decision: Decision(decision)})
	}
	_, err := dec.Token() // consume closing '}'
	return err
}

// MarshalJSON writes r back to the same "pattern": "decision" object
// shape UnmarshalJSON reads — needed because RuleList's default
// (struct-array) marshaling wouldn't round-trip through Load, which
// expects each tool's value to be an object mapping pattern to a plain
// decision string, not an array of {Pattern,Decision} objects.
func (r RuleList) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, rule := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(rule.Pattern)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		val, err := json.Marshal(string(rule.Decision))
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Config maps a tool name to its rules.
type Config map[string]RuleList

// HasAny reports whether cfg configures any rules at all — an empty
// (but present) permissions.json shouldn't trigger a trust prompt for
// nothing.
func (c Config) HasAny() bool {
	return len(c) > 0
}

// ConfigPath returns <workDir>/.chisel/permissions.json.
func ConfigPath(workDir string) string {
	return filepath.Join(workDir, ".chisel", "permissions.json")
}

// Load reads and parses workDir's permissions.json. A missing file is
// not an error — found reports whether one was present at all, so a
// caller can distinguish "nothing configured" from "failed to parse"
// the same way internal/hooks.LoadConfig does.
func Load(workDir string) (cfg Config, found bool, err error) {
	data, err := os.ReadFile(ConfigPath(workDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, true, err
	}
	return cfg, true, nil
}

// Add appends a new rule for toolName (pattern → decision) to cfg,
// returning the updated Config — cfg may be nil (no permissions.json
// existed yet, or none was configured). Doesn't write anything to disk;
// call Save with the result. Order matters (see RuleList's own doc
// comment on last-match-wins), so this always appends rather than
// inserting — a newly added rule is meant to be the most specific,
// most-recently-decided one for its pattern.
func Add(cfg Config, toolName, pattern string, decision Decision) Config {
	if cfg == nil {
		cfg = Config{}
	}
	cfg[toolName] = append(cfg[toolName], Rule{Pattern: pattern, Decision: decision})
	return cfg
}

// Save writes cfg to workDir's permissions.json, creating the .chisel
// directory if it doesn't exist yet.
func Save(workDir string, cfg Config) error {
	path := ConfigPath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Match returns the decision for toolName given argText — the text
// rules are matched against (a bash command's own text, for instance)
// — and whether any rule for that tool matched at all. Last-match-wins
// among the tool's rules, in declaration order.
func Match(cfg Config, toolName, argText string) (decision Decision, matched bool) {
	for _, r := range cfg[toolName] {
		if globMatch(r.Pattern, argText) {
			decision, matched = r.Decision, true
		}
	}
	return decision, matched
}

// globMatch reports whether pattern (with * matching any sequence of
// characters, including "/", and ? matching exactly one) matches text
// in full. Deliberately not path.Match/filepath.Match: those treat "/"
// as a path separator that "*" won't cross, which is exactly wrong
// here — these patterns match shell command text, which routinely
// contains "/" in file paths, branch names, and URLs that a rule like
// "git *" must still match completely.
func globMatch(pattern, text string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(text)
}

var trustStore = trust.Open("trusted_permrules.json")

// ContentHash returns a stable identifier for permissions.json's raw content.
func ContentHash(data []byte) string {
	return trust.ContentHash(data)
}

// IsTrusted reports whether hash has already been approved in a past run.
func IsTrusted(hash string) (bool, error) {
	return trustStore.IsTrusted(hash)
}

// Trust records hash as approved, persisting it for future runs.
func Trust(hash string) error {
	return trustStore.Trust(hash)
}
