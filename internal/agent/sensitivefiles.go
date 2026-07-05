package agent

import "path/filepath"

// sensitiveFilePatterns are filenames that commonly hold secrets —
// isSensitiveFile blocks read-only tools (view, grep) from surfacing
// their content into the model's context at all, the same way opencode
// blocks .env by default. Scoped to reads specifically, not the editor
// tool's create/str_replace/insert commands: a user legitimately asking
// the model to help populate a fresh .env from .env.example is a normal
// task chisel shouldn't lose, but there's no legitimate reason for the
// model to ever need an existing secret's actual value in its context.
// Deliberately an outright refusal, not a permission prompt: a
// subagent, the oracle, and headless mode all have no permission gate
// to fall back on at all, so a prompt here would mean nothing for
// exactly the callers most likely to hit this.
var sensitiveFilePatterns = []string{
	".env", ".env.*",
	"*.pem", "*_rsa", "*_dsa", "*_ed25519", "*_ecdsa",
	"credentials.json", ".npmrc", ".netrc",
}

// isSensitiveFile reports whether path's filename matches one of
// sensitiveFilePatterns — matched against the base name only, so
// secrets/.env and config/id_rsa are caught the same as top-level ones.
func isSensitiveFile(path string) bool {
	base := filepath.Base(path)
	for _, pattern := range sensitiveFilePatterns {
		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
	}
	return false
}
