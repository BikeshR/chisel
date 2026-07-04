package agent

import (
	"encoding/json"
	"os"

	"github.com/pmezard/go-difflib/difflib"
)

// PreviewEdit computes a unified diff of what a str_replace_based_edit_tool
// call would change, without writing anything — so the permission prompt
// can show it before the user decides. ok is false whenever a diff isn't
// applicable: any other tool, a "view" command, a no-op edit, or one whose
// preview can't be computed (e.g. old_str genuinely doesn't match) — the
// last case isn't fatal here, since the real error surfaces normally if
// the user approves it and it actually runs.
func PreviewEdit(workDir string, call ToolCall) (diff string, ok bool) {
	if call.Function.Name != "str_replace_based_edit_tool" {
		return "", false
	}

	var in editorInput
	if err := json.Unmarshal(call.input(), &in); err != nil {
		return "", false
	}
	if in.Command != "create" && in.Command != "str_replace" && in.Command != "insert" {
		return "", false
	}

	path, err := resolveInWorkDir(workDir, in.Path)
	if err != nil {
		return "", false
	}

	var before string
	if data, err := os.ReadFile(path); err == nil {
		before = string(data)
	}

	var after string
	switch in.Command {
	case "create":
		after = in.FileText
	case "str_replace":
		if after, err = applyStrReplace(before, in.OldStr, in.NewStr); err != nil {
			return "", false
		}
	case "insert":
		if after, _, err = applyInsert(before, in.InsertLine, in.InsertText); err != nil {
			return "", false
		}
	}

	if before == after {
		return "", false
	}

	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(before),
		B:        difflib.SplitLines(after),
		FromFile: in.Path,
		ToFile:   in.Path,
		Context:  3,
	})
	if err != nil {
		return "", false
	}
	return text, true
}
