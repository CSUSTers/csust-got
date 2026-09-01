package agentv3

const (
	agentV3DefaultBotName         = "bot"
	agentV3WorkspaceRootDefault   = "/workspace"
	agentV3RuntimeModeRemoteHTTP  = "remote_http"
	agentV3SkillsModeSystemPrompt = "system_prompt"

	agentV3ToolRead      = "read"
	agentV3ToolGrep      = "grep"
	agentV3ToolWrite     = "write"
	agentV3ToolEdit      = "edit"
	agentV3ToolBash      = "bash"
	agentV3ToolLoadSkill = "load_skill"

	agentV3ToolNameField      = "name"
	agentV3ToolDescField      = "desc"
	agentV3ToolArgsField      = "args"
	agentV3ToolCWDField       = "cwd"
	agentV3ToolCWDDescription = "Optional working directory, default /workspace"
	agentV3ToolPathField      = "path"
	agentV3ToolPatternField   = "pattern"
	agentV3ToolContentField   = "content"
	agentV3ToolPatchField     = "patch"
	agentV3ToolCommandField   = "command"
	agentV3ToolTimeoutField   = "timeout"
	agentV3ToolSkillNameField = "name"
)
