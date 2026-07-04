package tui

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain forces a real color profile for the whole package's tests —
// without it, lipgloss auto-detects go test's non-TTY stdout as
// colorless and silently strips every style's ANSI codes, which would
// make tests asserting on colorized output (colorizeDiff, in
// particular) pass or fail based on incidental terminal detection
// rather than the actual rendering logic.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

const sampleDiff = `--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,3 @@
 package main
-func old() {}
+func new() {}
`

func TestColorizeDiffAddsColorToContentLinesOnly(t *testing.T) {
	got := colorizeDiff(sampleDiff)
	lines := strings.Split(got, "\n")

	for _, l := range lines {
		switch {
		case strings.Contains(l, "+++") || strings.Contains(l, "---"):
			if strings.Contains(l, "\x1b[") {
				t.Errorf("file header line got colorized: %q", l)
			}
		case strings.Contains(l, "func new()"):
			if !strings.Contains(l, "\x1b[") {
				t.Errorf("added line not colorized: %q", l)
			}
		case strings.Contains(l, "func old()"):
			if !strings.Contains(l, "\x1b[") {
				t.Errorf("removed line not colorized: %q", l)
			}
		case strings.HasPrefix(l, " package main"):
			if strings.Contains(l, "\x1b[") {
				t.Errorf("context line got colorized: %q", l)
			}
		}
	}
}

func TestColorizeDiffCapsLongDiffs(t *testing.T) {
	var b strings.Builder
	total := maxDiffPreviewLines + 25
	for i := 0; i < total; i++ {
		b.WriteString("+line " + strconv.Itoa(i) + "\n")
	}

	got := colorizeDiff(b.String())
	lines := strings.Split(got, "\n")

	// maxDiffPreviewLines shown + 1 "N more lines" marker.
	if len(lines) != maxDiffPreviewLines+1 {
		t.Fatalf("got %d lines, want %d (cap + marker)", len(lines), maxDiffPreviewLines+1)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "25 more lines") {
		t.Errorf("last line = %q, want it to mention the 25 hidden lines", last)
	}
}

func TestColorizeDiffDoesNotTruncateShortDiffs(t *testing.T) {
	got := colorizeDiff(sampleDiff)
	if strings.Contains(got, "more lines") {
		t.Errorf("short diff got a truncation marker: %q", got)
	}
}
