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
// of text every time.
func expandFileReferences(workDir, text string) string {
	return fileRefPattern.ReplaceAllStringFunc(text, func(match string) string {
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
		return fmt.Sprintf("\n--- %s ---\n%s\n--- end %s ---\n", path, content, path)
	})
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

// completeFileReferenceInInput replaces the last @-prefixed token in
// the textarea's current value with its tab-completed form, if exactly
// one or more files match. A no-op if there's no trailing @token, or if
// completion wouldn't change anything (no matches, or already at the
// longest common prefix).
func (m *Model) completeFileReferenceInInput() {
	value := m.textArea.Value()
	loc := lastFileRefToken.FindStringSubmatchIndex(value)
	if loc == nil {
		return
	}
	partial := value[loc[2]:loc[3]]
	completed, ok := completeFileReference(m.workDir, partial)
	if !ok || completed == partial {
		return
	}
	m.textArea.SetValue(value[:loc[2]] + completed)
}
