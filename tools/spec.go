package tools

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

type ToolParameters struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required"`
}

type Property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func All() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "shell",
			Description: "Run a shell command in the workspace. Returns stdout and stderr. Use this to read files, run tests, apply patches, install deps — anything you'd do in a terminal.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: map[string]Property{
					"command": {
						Type:        "string",
						Description: "The shell command to run (passed to /bin/sh -c).",
					},
					"timeout_ms": {
						Type:        "integer",
						Description: "Optional timeout in milliseconds. Defaults to 10000.",
					},
				},
				Required: []string{"command"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read a file from the workspace and return its contents with 1-indexed line numbers. Prefer this over `shell cat` for reading source code — it's cheaper and gives you line references for targeted edits.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "Path to the file (relative to workspace root).",
					},
					"start_line": {
						Type:        "integer",
						Description: "First line to return (1-indexed, inclusive). Omit for beginning of file.",
					},
					"end_line": {
						Type:        "integer",
						Description: "Last line to return (1-indexed, inclusive). Omit for end of file.",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write (or overwrite) a file in the workspace with new contents.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "Path to the file (relative to workspace root).",
					},
					"content": {
						Type:        "string",
						Description: "The full new content of the file.",
					},
				},
				Required: []string{"path", "content"},
			},
		},
		{
			Name:        "list_dir",
			Description: "List entries in a directory. Returns names, types (file/dir), and sizes.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "Directory path (relative to workspace root). Defaults to '.'.",
					},
				},
				Required: []string{},
			},
		},
	}
}
