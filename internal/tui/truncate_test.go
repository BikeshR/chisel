package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunesIsUTF8Safe(t *testing.T) {
	// 130 multi-byte (3-byte each) characters — a byte-based s[:120] would
	// land mid-character; this must cut on a rune boundary instead.
	s := strings.Repeat("世", 130)

	got, ok := truncateRunes(s, 120)
	if !ok {
		t.Fatal("ok = false, want true — the input is longer than the limit")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 120 {
		t.Errorf("rune count = %d, want 120", n)
	}
}

func TestTruncateRunesUnderLimit(t *testing.T) {
	got, ok := truncateRunes("short", 120)
	if ok {
		t.Error("ok = true for a string under the limit")
	}
	if got != "short" {
		t.Errorf("got = %q, want unchanged", got)
	}
}

func TestFirstLineUTF8Safe(t *testing.T) {
	s := strings.Repeat("世", 130)
	got := firstLine(s)
	if !utf8.ValidString(got) {
		t.Fatalf("firstLine output is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("got = %q, want a truncation marker", got)
	}
}

func TestCommitMessageUTF8Safe(t *testing.T) {
	s := strings.Repeat("世", 80)
	got := commitMessage(s)
	if !utf8.ValidString(got) {
		t.Fatalf("commitMessage output is not valid UTF-8: %q", got)
	}
}
