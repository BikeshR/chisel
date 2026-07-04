package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BikeshR/chisel/internal/agent"
)

// fileRefPattern matches an @-prefixed file reference: "@" followed by
// one or more non-whitespace characters. Deliberately permissive — an
// "@word" that doesn't resolve to a real file inside workDir is left as
// literal text (see expandFileReferences), so treating any @token as a
// candidate costs nothing when it turns out to just be prose ("ask
// @someone about this").
var fileRefPattern = regexp.MustCompile(`@(\S+)`)

// expandFileReferences scans text for @path tokens and replaces each
// with the referenced file's content, wrapped so the model can tell
// where the injected content starts/ends and which path it came from.
// Only affects what's sent to the model (see submitText) — the
// transcript still shows exactly what the user typed, not the expanded
// form, so a large injected file doesn't turn the display into a wall
// of text every time. Injected content is capped the same way a tool
// result already is (agent.TruncateOutput) — without this, an
// @-referenced multi-megabyte log (or an accidentally-referenced
// binary) bypassed that cap entirely and invisibly: the whole point of
// capping tool output at maxToolOutputChars is that oversized content
// gets resent on *every* subsequent request in the conversation, and an
// @-reference is otherwise the one path into the context window with no
// bound at all. truncated reports which referenced paths actually hit
// the cap, so submitText can still surface that to the user even though
// the transcript itself only ever shows what they typed.
func expandFileReferences(workDir, text string) (expanded string, truncated []string) {
	expanded = fileRefPattern.ReplaceAllStringFunc(text, func(match string) string {
		path := strings.TrimPrefix(match, "@")
		content, err := agent.ReadFileInWorkDir(workDir, path)
		if err != nil {
			// Missing, escapes workDir, is a directory, whatever the
			// reason — leave the literal "@path" text rather than
			// silently dropping it or failing the whole submission;
			// the model still sees what the user typed and can ask
			// about it if that matters.
			return match
		}
		capped := agent.TruncateOutput(content)
		if capped != content {
			truncated = append(truncated, path)
		}
		return fmt.Sprintf("\n--- %s ---\n%s\n--- end %s ---\n", path, capped, path)
	})
	return expanded, truncated
}

// fileRefSkipDirs is the set of directories @file tab completion never
// walks into. Deliberately separate from search.go's skipDirs (a
// smaller, local list is fine here — this is about not making
// interactive completion slow/noisy on the same handful of huge,
// uninteresting directories, not about exhaustive search correctness).
var fileRefSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true,
}

// listFilesForCompletion walks workDir and returns every file path
// (relative to workDir, "/"-separated, sorted) whose path starts with
// prefix.
func listFilesForCompletion(workDir, prefix string) []string {
	var matches []string
	_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == workDir {
			return nil
		}
		if d.IsDir() {
			if fileRefSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, prefix) {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	return matches
}

// completeFileReference completes partial (the text typed after "@",
// without the "@" itself) to the longest common prefix among matching
// files under workDir — standard shell-style tab completion: a single
// match completes fully, several matches complete only as far as they
// all agree.
func completeFileReference(workDir, partial string) (completed string, ok bool) {
	matches := listFilesForCompletion(workDir, partial)
	if len(matches) == 0 {
		return "", false
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return longestCommonPrefix(matches), true
}

func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// lastFileRefToken finds the @-prefixed token at the very end of the
// current input — tab completion is based on the *last* token in the
// whole input rather than precise cursor position, since textarea
// doesn't expose per-line cursor column simply, and an @-reference is
// overwhelmingly typed right before submitting, not edited mid-line
// after more text follows it.
var lastFileRefToken = regexp.MustCompile(`@(\S*)$`)

// maxCompletionCandidatesShown caps how many candidates an ambiguous tab
// completion lists in the transcript — a prefix matching hundreds of
// files is still worth completing as far as possible, but dumping all of
// them isn't a usable list, just noise.
const maxCompletionCandidatesShown = 20

// completeFileReferenceInInput replaces the last @-prefixed token in
// the textarea's current value with its tab-completed form, if exactly
// one or more files match. A no-op if there's no trailing @token, or if
// completion wouldn't change anything (no matches, or already at the
// longest common prefix). When several files match, the common-prefix
// completion alone gave no feedback about what those matches actually
// were — this also lists them (capped) in the transcript.
func (m *Model) completeFileReferenceInInput() {
	value := m.textArea.Value()
	loc := lastFileRefToken.FindStringSubmatchIndex(value)
	if loc == nil {
		return
	}
	partial := value[loc[2]:loc[3]]
	completed, ok := completeFileReference(m.workDir, partial)
	if !ok {
		return
	}
	if completed != partial {
		m.textArea.SetValue(value[:loc[2]] + completed)
	}
	if matches := listFilesForCompletion(m.workDir, partial); len(matches) > 1 {
		m.appendLine(dimStyle.Render("  " + strings.Join(capCandidates(matches, maxCompletionCandidatesShown), "  ")))
	}
}

// capCandidates truncates matches to at most max entries, appending a
// count of what was hidden rather than silently dropping it.
func capCandidates(matches []string, max int) []string {
	if len(matches) <= max {
		return matches
	}
	shown := append([]string{}, matches[:max]...)
	return append(shown, fmt.Sprintf("… %d more", len(matches)-max))
}
