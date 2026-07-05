package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const grepResultLimit = 200
const globResultLimit = 200

// skipDirs is a fixed list of directory names grep/glob never descend
// into — build output and dependency trees that would otherwise flood
// results in most non-Go repos (the original four entries here covered
// Go/Node/Python well enough, but nothing else). This is a coarser tool
// than respecting .gitignore properly (arbitrary patterns, negation,
// per-directory files) — that would catch more, and precisely what a
// given project actually wants excluded, but at real implementation
// and correctness risk for a search tool; a fixed list of well-known
// noisy directory names is the deliberately simpler bar chisel clears
// today.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"dist": true, "build": true, "target": true, ".next": true, "__pycache__": true,
	".pytest_cache": true, ".mypy_cache": true, ".tox": true, ".gradle": true,
	".terraform": true, "bower_components": true, ".cache": true, "coverage": true,
}

func globTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "glob",
			Description: "Find files by glob pattern (supports ** for recursive matching), relative to the working directory. Returns matching paths sorted.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": `Glob pattern, e.g. "**/*.go" or "internal/**/*_test.go"`,
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

func grepTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "grep",
			Description: "Search file contents by regular expression across the working directory. Returns matching lines as path:line:text, capped at the first " + fmt.Sprint(grepResultLimit) + " matches.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regular expression to search for (RE2 syntax)",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": `Optional glob to restrict which files are searched, e.g. "**/*.go"`,
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

func runGlob(workDir string, rawInput json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}

	matches, err := doublestar.Glob(os.DirFS(workDir), in.Pattern)
	if err != nil {
		return "", err
	}

	// os.DirFS doesn't defend against symlinks pointing outside workDir
	// (its own docs say as much) — filter out anything that resolves
	// elsewhere, the same check every other filesystem tool goes through.
	// Also apply skipDirs here — unlike runGrep's filepath.WalkDir,
	// doublestar.Glob has no directory-pruning hook of its own, so a
	// pattern like "**/*.js" in a repo with node_modules would otherwise
	// return everything underneath it too.
	safe := matches[:0]
	for _, m := range matches {
		if pathHasSkipDir(m) {
			continue
		}
		if _, err := resolveInWorkDir(workDir, m); err == nil {
			safe = append(safe, m)
		}
	}
	matches = safe

	if len(matches) == 0 {
		return "(no matches)", nil
	}
	sort.Strings(matches)

	if len(matches) > globResultLimit {
		shown := matches[:globResultLimit]
		return fmt.Sprintf("%s\n… truncated at %d matches (%d more)", strings.Join(shown, "\n"), globResultLimit, len(matches)-globResultLimit), nil
	}
	return strings.Join(matches, "\n"), nil
}

// pathHasSkipDir reports whether any path segment of p (fs.FS-style,
// always "/"-separated regardless of OS) is one of skipDirs.
func pathHasSkipDir(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

func runGrep(workDir string, rawInput json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}

	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}

	var results []string
	var scanErrors []string
	err = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A permission-denied (or similarly unreadable) directory or
			// file must not abort the whole walk — one root-owned
			// subdirectory (a docker volume mount, a stray .cache with
			// odd perms) would otherwise make grep fail across the
			// entire repo instead of just skipping that one entry.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(results) >= grepResultLimit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		if in.Glob != "" {
			ok, err := doublestar.Match(in.Glob, rel)
			if err != nil || !ok {
				return nil
			}
		}

		// WalkDir visits a symlink as a leaf entry without following it
		// for traversal, but opening it directly (as below) would follow
		// it to wherever it points — resolveInWorkDir is what every other
		// filesystem tool goes through to reject that; grep skipped it.
		resolved, err := resolveInWorkDir(workDir, rel)
		if err != nil {
			return nil // escapes the working directory — skip silently, same as an unreadable file
		}
		if isSensitiveFile(resolved) {
			return nil // matches a known-secret filename (see isSensitiveFile) — skip silently, same treatment as an unreadable file
		}

		f, err := os.Open(resolved)
		if err != nil {
			return nil // unreadable file, skip
		}
		defer func() { _ = f.Close() }()

		if isBinary(f) {
			return nil
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if re.MatchString(scanner.Text()) {
				results = append(results, fmt.Sprintf("%s:%d:%s", rel, lineNum, scanner.Text()))
				if len(results) >= grepResultLimit {
					break
				}
			}
		}
		// scanner.Err() is nil for a clean EOF — the only other cause is a
		// single line exceeding the 1MB buffer above, which stops Scan()
		// silently partway through the file. Without checking this, a
		// file with one over-long line was searched only up to that
		// point, with grep reporting a normal, complete-looking result
		// that just happened to be missing everything after it.
		if scanErr := scanner.Err(); scanErr != nil {
			scanErrors = append(scanErrors, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(results) == 0 && len(scanErrors) == 0 {
		return "(no matches)", nil
	}
	if len(results) >= grepResultLimit {
		results = append(results, fmt.Sprintf("… truncated at %d matches", grepResultLimit))
	}
	if len(scanErrors) > 0 {
		results = append(results, fmt.Sprintf("(note: %d file(s) had a line too long to fully scan and may be missing matches: %s)",
			len(scanErrors), strings.Join(scanErrors, ", ")))
	}
	return strings.Join(results, "\n"), nil
}

// isBinary sniffs the first chunk of an already-open file for a NUL byte,
// then rewinds it for the caller. A crude but standard heuristic.
func isBinary(f *os.File) bool {
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	defer func() { _, _ = f.Seek(0, 0) }()
	return looksBinary(buf[:n])
}

// looksBinary applies the same NUL-byte heuristic as isBinary directly
// to an in-memory buffer, for callers (viewPath, in editor.go) that
// already have the full content read rather than an open file handle.
func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}
