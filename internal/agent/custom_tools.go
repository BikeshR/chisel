package agent

// buildTools assembles chisel's tool set: bash and file editing (schema
// written by hand, matching Anthropic's bash_20250124/text_editor_20250728
// tool shapes closely enough that Execute()'s dispatch and bash.go/
// editor.go's execution code need no awareness of the change) plus glob
// and grep (search.go).
func buildTools() []Tool {
	return []Tool{
		bashTool(),
		editorTool(),
		globTool(),
		grepTool(),
		subagentDispatchTool(),
	}
}

func subagentDispatchTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "dispatch_subagent",
			Description: "Delegate a self-contained research or exploration task to a subagent with a narrower, read-only tool set (glob, grep, view — no edits, no shell commands, no further subagents). Use this to investigate something without cluttering your own context with every intermediate exploration step; you get back one concise final answer. Good for open-ended \"find out how X works\" or \"search for all usages of Y and summarize\" tasks. The subagent starts fresh with no access to this conversation, so describe the task fully and self-contained.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{
						"type":        "string",
						"description": "A complete, self-contained description of what to investigate and report back on.",
					},
				},
				"required": []string{"task"},
			},
		},
	}
}

func bashTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "bash",
			Description: "Execute a bash command in the working directory. Returns combined stdout and stderr.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to run",
					},
					"restart": map[string]any{
						"type":        "boolean",
						"description": "Set true to restart the shell session instead of running a command",
					},
				},
			},
		},
	}
}

func editorTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name: "str_replace_based_edit_tool",
			Description: "View, create, and edit files. Commands: " +
				"view (read a file, optionally a [start,end] line range, or list a directory), " +
				"create (write a new file, backing up any existing one first), " +
				"str_replace (replace exactly one occurrence of old_str with new_str — errors if old_str matches zero or more than once), " +
				"insert (insert insert_text after line insert_line, 0 = start of file).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type": "string",
						"enum": []string{"view", "create", "str_replace", "insert"},
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Path relative to the working directory",
					},
					"file_text": map[string]any{
						"type":        "string",
						"description": "Full file contents — required for create",
					},
					"old_str": map[string]any{
						"type":        "string",
						"description": "Exact text to replace — required for str_replace",
					},
					"new_str": map[string]any{
						"type":        "string",
						"description": "Replacement text — required for str_replace",
					},
					"insert_line": map[string]any{
						"type":        "integer",
						"description": "Line number to insert after — required for insert",
					},
					"insert_text": map[string]any{
						"type":        "string",
						"description": "Text to insert — required for insert",
					},
					"view_range": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Optional [start, end] line range for view (end -1 means end of file)",
					},
				},
				"required": []string{"command", "path"},
			},
		},
	}
}
