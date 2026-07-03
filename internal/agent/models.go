package agent

// KnownModels lists the model IDs the /model command offers. It's a
// static, hand-maintained catalog rather than a live lookup — every model
// OpenCode Go exposed via its /v1/models endpoint at the time this was
// written (confirmed 2026-07, https://opencode.ai/zen/go/v1/models). Bare
// IDs — chisel is OpenCode-Go-only now, so there's no routing prefix to
// strip before the request goes on the wire.
func KnownModels() []string {
	return []string{
		"minimax-m3",
		"minimax-m2.7",
		"minimax-m2.5",
		"kimi-k2.7-code",
		"kimi-k2.6",
		"kimi-k2.5",
		"glm-5.2",
		"glm-5.1",
		"glm-5",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"qwen3.7-max",
		"qwen3.7-plus",
		"qwen3.6-plus",
		"qwen3.5-plus",
		"mimo-v2-pro",
		"mimo-v2-omni",
		"mimo-v2.5-pro",
		"mimo-v2.5",
		"hy3-preview",
	}
}
