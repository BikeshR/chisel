package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
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

func createFile(path, content string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		existing, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path+".bak", existing, 0o644); err != nil {
			return "", fmt.Errorf("backing up existing file: %w", err)
		}
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
		return "", fmt.Errorf("old_str not found")
	case 1:
		return strings.Replace(content, oldStr, newStr, 1), nil
	default:
		return "", fmt.Errorf("old_str matches %d times; must match exactly once", count)
	}
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
