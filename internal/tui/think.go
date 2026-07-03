package tui

import "strings"

const thinkOpenTag = "<think>"
const thinkCloseTag = "</think>"

// renderAssistantText transforms raw model output for display. Some models
// (observed: MiniMax) emit their reasoning inline in the content text as
// <think>...</think>, rather than as a separate field — there's no
// structured way to detect it ahead of time. Collapsed by default to a
// one-line indicator; shown in full when showThinking is on.
//
// Re-scans the whole accumulated string on every call rather than tracking
// incremental state, so a <think>/</think> tag split across two streamed
// chunks is handled for free — by the time both halves exist anywhere in
// the buffer, a plain substring search finds them.
func renderAssistantText(raw string, showThinking bool) string {
	if showThinking {
		return raw
	}

	var b strings.Builder
	rest := raw
	for {
		start := strings.Index(rest, thinkOpenTag)
		if start == -1 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])

		afterOpen := rest[start+len(thinkOpenTag):]
		end := strings.Index(afterOpen, thinkCloseTag)
		if end == -1 {
			// Still streaming the thinking block — nothing closed yet.
			b.WriteString(dimStyle.Render("⌄ thinking… (/think to show)"))
			break
		}
		b.WriteString(dimStyle.Render("⌄ thinking (/think to show)"))
		rest = afterOpen[end+len(thinkCloseTag):]
	}
	return b.String()
}
