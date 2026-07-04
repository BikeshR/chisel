package agent

import (
	"strings"
	"testing"
)

func TestApplyStrReplaceSingleMatch(t *testing.T) {
	got, err := applyStrReplace("hello world", "world", "there")
	if err != nil {
		t.Fatalf("applyStrReplace: %v", err)
	}
	if got != "hello there" {
		t.Errorf("got = %q, want %q", got, "hello there")
	}
}

func TestApplyStrReplaceZeroMatchPlainNotFound(t *testing.T) {
	_, err := applyStrReplace("hello world", "goodbye", "hi")
	if err == nil {
		t.Fatal("expected an error for a non-matching old_str")
	}
	if !strings.Contains(err.Error(), "old_str not found") {
		t.Errorf("err = %q, want it to say old_str not found", err)
	}
	if strings.Contains(err.Error(), "whitespace-insensitive") {
		t.Errorf("err = %q, want no whitespace-insensitive hint when there's genuinely no near-miss", err)
	}
}

// TestApplyStrReplaceZeroMatchWhitespaceNearMiss is the regression test
// for the most common real str_replace failure — the model's old_str
// uses tabs where the file uses spaces (or vice versa, or different
// indentation depth) — which should get a more actionable error than a
// bare "not found".
func TestApplyStrReplaceZeroMatchWhitespaceNearMiss(t *testing.T) {
	content := "func f() {\n\tif true {\n\t\treturn\n\t}\n}"
	oldStr := "func f() {\n    if true {\n        return\n    }\n}" // same structure, spaces not tabs
	_, err := applyStrReplace(content, oldStr, "replacement")
	if err == nil {
		t.Fatal("expected an error — old_str doesn't match verbatim")
	}
	if !strings.Contains(err.Error(), "whitespace-insensitive") {
		t.Errorf("err = %q, want it to flag the whitespace-insensitive near-miss", err)
	}
}

func TestApplyStrReplaceMultiMatchListsLineNumbers(t *testing.T) {
	content := "foo\nbar\nfoo\nbaz\nfoo"
	_, err := applyStrReplace(content, "foo", "qux")
	if err == nil {
		t.Fatal("expected an error for old_str matching more than once")
	}
	msg := err.Error()
	if !strings.Contains(msg, "3 times") {
		t.Errorf("err = %q, want it to report 3 matches", msg)
	}
	for _, line := range []string{"1", "3", "5"} {
		if !strings.Contains(msg, line) {
			t.Errorf("err = %q, want it to mention line %s", msg, line)
		}
	}
}

func TestApplyStrReplaceMultiMatchSingularWording(t *testing.T) {
	content := "foo\nfoo"
	_, err := applyStrReplace(content, "foo", "bar")
	msg := err.Error()
	if !strings.Contains(msg, "at lines 1, 2") {
		t.Errorf("err = %q, want plural \"lines\" for 2 matches", msg)
	}
}
