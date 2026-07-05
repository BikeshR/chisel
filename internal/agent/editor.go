package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type editorInput struct {
	Command    string `json:"command"`
	Path       string `json:"path"`
	FileText   string `json:"file_text"`
	OldStr     string `json:"old_str"`
	NewStr     string `json:"new_str"`
	InsertLine int    `json:"insert_line"`
	InsertText string `json:"insert_text"`
	ViewRange  []int  `json:"view_range"`
}

// runEditor implements Anthropic's str_replace_based_edit_tool commands
// against the local filesystem, confined to workDir.
func runEditor(workDir string, rawInput json.RawMessage) (string, error) {
	var in editorInput
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}

	path, err := resolveInWorkDir(workDir, in.Path)
	if err != nil {
		return "", err
	}

	switch in.Command {
	case "view":
		return viewPath(path, in.ViewRange)
	case "create":
		return createFile(path, in.FileText)
	case "str_replace":
		return strReplace(path, in.OldStr, in.NewStr)
	case "insert":
		return insertText(path, in.InsertLine, in.InsertText)
	default:
		return "", fmt.Errorf("unknown editor command %q", in.Command)
	}
}

func viewPath(path string, viewRange []int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	}

	if isSensitiveFile(path) {
		return "", fmt.Errorf("%s looks like it may hold secrets (matches a pattern like .env, *.pem, credentials.json) — view refuses to read it", filepath.Base(path))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if looksBinary(data) {
		return "", fmt.Errorf("%s appears to be a binary file — view only supports text", path)
	}
	lines := strings.Split(string(data), "\n")

	start, end := 1, len(lines)
	if len(viewRange) == 2 {
		start, end = viewRange[0], viewRange[1]
		if end == -1 {
			end = len(lines)
		}
	}
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
	}
	return b.String(), nil
}

// createFile writes content to path, creating any missing intermediate
// directories along the way (resolveInWorkDir validates where a
// not-yet-existing nested path would land, but doesn't create anything
// itself — that's this function's job). No .bak backup on overwrite —
// that behavior predates the permission prompt's diff preview and
// /git auto; with both in place, a backup file chisel never mentions,
// never cleans up, and /git auto happily commits alongside everything
// else had lost its reason to exist.
func createFile(path, content string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s", path), nil
}

func strReplace(path, oldStr, newStr string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	updated, err := applyStrReplace(string(data), oldStr, newStr)
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, path)
	}

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced 1 occurrence in %s", path), nil
}

// applyStrReplace computes the post-replacement content without touching
// disk, so PreviewEdit (diff.go) can show what a str_replace would do
// before it's approved, using the exact same logic that will actually run.
func applyStrReplace(content, oldStr, newStr string) (string, error) {
	count := strings.Count(content, oldStr)
	switch count {
	case 0:
		return "", zeroMatchError(content, oldStr)
	case 1:
		return strings.Replace(content, oldStr, newStr, 1), nil
	default:
		return "", multiMatchError(content, oldStr, count)
	}
}

// zeroMatchError reports old_str matching nothing, checking for the
// single most common near-miss cause first — a whitespace difference
// (tabs vs spaces, trailing spaces, a differently-indented paste) — since
// a bare "not found" leaves the model to guess why and costs it a fresh
// view call just to start diagnosing, every time this happens.
func zeroMatchError(content, oldStr string) error {
	if strings.Contains(normalizeWhitespace(content), normalizeWhitespace(oldStr)) {
		return fmt.Errorf("old_str not found verbatim, but a whitespace-insensitive match exists nearby — check for tabs vs spaces or a different indentation level")
	}
	return fmt.Errorf("old_str not found")
}

// normalizeWhitespace collapses each line's leading/trailing whitespace
// and any internal run of whitespace down to single spaces — the
// whitespace-insensitive comparison zeroMatchError uses to detect an
// indentation/tabs-vs-spaces near-miss.
func normalizeWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.Join(lines, "\n")
}

// multiMatchError reports old_str matching more than once, listing the
// 1-based line number each match starts on so the model can add
// disambiguating context on its next attempt without first needing a
// separate view call just to find out where the matches even are.
func multiMatchError(content, oldStr string, count int) error {
	lineNumbers := make([]string, 0, count)
	searchFrom := 0
	for range count {
		idx := strings.Index(content[searchFrom:], oldStr)
		if idx == -1 {
			break
		}
		absolute := searchFrom + idx
		lineNumbers = append(lineNumbers, strconv.Itoa(strings.Count(content[:absolute], "\n")+1))
		searchFrom = absolute + len(oldStr)
	}
	return fmt.Errorf("old_str matches %d times (at line%s %s); must match exactly once — add more surrounding context to disambiguate",
		count, plural(count), strings.Join(lineNumbers, ", "))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func insertText(path string, afterLine int, text string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	result, inserted, err := applyInsert(string(data), afterLine, text)
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, path)
	}

	if err := os.WriteFile(path, []byte(result), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("inserted %d line(s) after line %d in %s", inserted, afterLine, path), nil
}

// applyInsert computes the post-insert content without touching disk —
// see applyStrReplace.
func applyInsert(content string, afterLine int, text string) (result string, insertedLines int, err error) {
	lines := strings.Split(content, "\n")

	if afterLine < 0 || afterLine > len(lines) {
		return "", 0, fmt.Errorf("insert_line %d out of range (file has %d lines)", afterLine, len(lines))
	}

	inserted := strings.Split(text, "\n")
	merged := make([]string, 0, len(lines)+len(inserted))
	merged = append(merged, lines[:afterLine]...)
	merged = append(merged, inserted...)
	merged = append(merged, lines[afterLine:]...)

	return strings.Join(merged, "\n"), len(inserted), nil
}
