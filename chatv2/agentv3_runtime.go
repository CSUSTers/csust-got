//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var (
	errRuntimeEndpointEmpty     = errors.New("agent v3 runtime endpoint is empty")
	errRuntimeHTTPStatus        = errors.New("agent v3 runtime returned non-success status")
	errRuntimeClientUnspecified = errors.New("runtime client is not configured")
)

// RemoteRuntimeClient calls the agent-v3 remote runtime service.
type RemoteRuntimeClient struct {
	Endpoint       string
	AuthToken      string
	HTTPClient     *http.Client
	CommandTimeout time.Duration
	MaxOutputChars int
}

type runtimeCommonRequest struct {
	Namespace string `json:"namespace"`
	RunID     string `json:"run_id"`
	Cwd       string `json:"cwd,omitempty"`
}

type runtimeReadRequest struct {
	runtimeCommonRequest
	Path string `json:"path"`
}

type runtimeGrepRequest struct {
	runtimeCommonRequest
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

type runtimeWriteRequest struct {
	runtimeCommonRequest
	Path    string `json:"path"`
	Content string `json:"content"`
}

type runtimeEditRequest struct {
	runtimeCommonRequest
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

type runtimeBashRequest struct {
	runtimeCommonRequest
	Command string `json:"command"`
	Timeout string `json:"timeout,omitempty"`
}

type runtimeResetRequest struct {
	runtimeCommonRequest
}

type runtimeTextResponse struct {
	Content   string `json:"content,omitempty"`
	Output    string `json:"output,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

type runtimeBashResponse struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
	Error      string `json:"error,omitempty"`
}

type runtimeStatusResponse struct {
	OK            bool   `json:"ok"`
	Version       string `json:"version,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	SkillsRoot    string `json:"skills_root,omitempty"`
	BashSandbox   string `json:"bash_sandbox,omitempty"`
	Error         string `json:"error,omitempty"`
}

type runtimeResetResponse struct {
	OK            bool   `json:"ok"`
	NamespaceHash string `json:"namespace_hash,omitempty"`
	Removed       bool   `json:"removed"`
	Error         string `json:"error,omitempty"`
}

// NewRemoteRuntimeClient builds an agent-v3 remote runtime client.
func NewRemoteRuntimeClient(cfg *config.AgentV3RuntimeConfig, commandTimeout, requestTimeout time.Duration) *RemoteRuntimeClient {
	endpoint := "http://agent-runtime:8080"
	authEnv := ""
	maxOutput := 12000
	if cfg != nil {
		if cfg.Endpoint != "" {
			endpoint = cfg.Endpoint
		}
		authEnv = cfg.AuthTokenEnv
		if cfg.MaxOutputChars > 0 {
			maxOutput = cfg.MaxOutputChars
		}
	}
	if commandTimeout <= 0 {
		commandTimeout = 120 * time.Second
	}
	if requestTimeout <= 0 {
		requestTimeout = commandTimeout
	}
	token := ""
	if authEnv != "" {
		token = os.Getenv(authEnv)
	}
	return &RemoteRuntimeClient{
		Endpoint:       strings.TrimRight(endpoint, "/"),
		AuthToken:      token,
		HTTPClient:     &http.Client{Timeout: requestTimeout},
		CommandTimeout: commandTimeout,
		MaxOutputChars: maxOutput,
	}
}

// Read reads a file through the remote runtime service.
func (c *RemoteRuntimeClient) Read(ctx context.Context, req runtimeReadRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/read", req, &out)
	out.Content, out.Truncated = truncateForModel(out.Content, c.MaxOutputChars, out.Truncated)
	return out, err
}

// Grep searches the remote runtime workspace.
func (c *RemoteRuntimeClient) Grep(ctx context.Context, req runtimeGrepRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/grep", req, &out)
	if out.Output == "" {
		out.Output = out.Content
	}
	out.Output, out.Truncated = truncateForModel(out.Output, c.MaxOutputChars, out.Truncated)
	return out, err
}

// Write writes a file through the remote runtime service.
func (c *RemoteRuntimeClient) Write(ctx context.Context, req runtimeWriteRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/write", req, &out)
	return out, err
}

// Edit applies a patch in the remote runtime workspace.
func (c *RemoteRuntimeClient) Edit(ctx context.Context, req runtimeEditRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/edit", req, &out)
	return out, err
}

// Bash runs a command in the remote runtime workspace.
func (c *RemoteRuntimeClient) Bash(ctx context.Context, req runtimeBashRequest) (runtimeBashResponse, error) {
	var out runtimeBashResponse
	err := c.post(ctx, "/v1/bash", req, &out)
	out.Stdout, out.Truncated = truncateForModel(out.Stdout, c.MaxOutputChars, out.Truncated)
	out.Stderr, out.Truncated = truncateForModel(out.Stderr, c.MaxOutputChars, out.Truncated)
	return out, err
}

// Status returns remote runtime health information.
func (c *RemoteRuntimeClient) Status(ctx context.Context) (runtimeStatusResponse, error) {
	var out runtimeStatusResponse
	err := c.get(ctx, "/v1/status", &out)
	return out, err
}

// Reset removes the runtime workspace for a namespace.
func (c *RemoteRuntimeClient) Reset(ctx context.Context, req runtimeResetRequest) (runtimeResetResponse, error) {
	var out runtimeResetResponse
	err := c.post(ctx, "/v1/reset", req, &out)
	return out, err
}

func (c *RemoteRuntimeClient) post(ctx context.Context, path string, in any, out any) error {
	if c == nil || c.Endpoint == "" {
		return errRuntimeEndpointEmpty
	}
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	u, err := url.JoinPath(c.Endpoint, strings.TrimPrefix(path, "/"))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: runtime %s returned %d: %s", errRuntimeHTTPStatus, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *RemoteRuntimeClient) get(ctx context.Context, path string, out any) error {
	if c == nil || c.Endpoint == "" {
		return errRuntimeEndpointEmpty
	}
	u, err := url.JoinPath(c.Endpoint, strings.TrimPrefix(path, "/"))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: runtime %s returned %d: %s", errRuntimeHTTPStatus, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func truncateForModel(s string, limit int, already bool) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, already
	}
	end := limit
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	if end <= 0 {
		return "\n[truncated by bot]", true
	}
	return s[:end] + "\n[truncated by bot]", true
}

func buildAgentV3Tools(chatCfg *config.ChatConfigSingle, cfg *config.AgentV3Config) []tool.BaseTool {
	tools := []tool.BaseTool{
		&remoteReadTool{},
		&remoteGrepTool{},
		&remoteWriteTool{},
		&remoteEditTool{},
		&remoteBashTool{},
	}
	if agentV3RichSkillAvailable(chatCfg, cfg) {
		tools = append(tools, &loadSkillTool{})
	}
	return tools
}

func agentV3ToolDefinitionsText(includeLoadSkill bool) string {
	infos := []map[string]any{
		{agentV3ToolNameField: agentV3ToolRead, agentV3ToolArgsField: agentV3ToolPathField, agentV3ToolDescField: "Read a file from /workspace."},
		{agentV3ToolNameField: agentV3ToolGrep, agentV3ToolArgsField: "pattern,path?", agentV3ToolDescField: "Search literal or regex text in /workspace."},
		{agentV3ToolNameField: agentV3ToolWrite, agentV3ToolArgsField: "path,content", agentV3ToolDescField: "Write a file under /workspace."},
		{agentV3ToolNameField: agentV3ToolEdit, agentV3ToolArgsField: "path,patch", agentV3ToolDescField: "Apply a unified diff patch to a file under /workspace."},
		{agentV3ToolNameField: agentV3ToolBash, agentV3ToolArgsField: "command,cwd?,timeout?", agentV3ToolDescField: "Run a shell command in the remote runtime namespace. Common utilities include curl, jq, git, tar, gzip, unzip, file, sed, grep, find, and coreutils."},
	}
	if includeLoadSkill {
		infos = append(infos, map[string]any{
			agentV3ToolNameField: agentV3ToolLoadSkill,
			agentV3ToolArgsField: "name",
			agentV3ToolDescField: "Load an agent-v3 built-in skill for the next answer. For rich-message, this MUST be the LAST tool call immediately before the final <telegram_rich_message>; no other tool or assistant text may come between them.",
		})
	}
	data, _ := json.Marshal(infos)
	return string(data)
}

type remoteReadTool struct{}

type remoteReadArgs struct {
	Path string `json:"path"`
	Cwd  string `json:"cwd,omitempty"`
}

func (t *remoteReadTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentV3ToolRead,
		Desc: "Read a file from the remote runtime workspace.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			agentV3ToolPathField: {Type: "string", Desc: "Workspace file path, e.g. /workspace/file.txt", Required: true},
			agentV3ToolCWDField:  {Type: "string", Desc: agentV3ToolCWDDescription},
		}),
	}, nil
}

func (t *remoteReadTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, agentV3ToolRead)
	if err != nil {
		return "", err
	}
	var args remoteReadArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("read: invalid arguments: %w", err)
	}
	resp, err := tc.RuntimeClient.Read(ctx, runtimeReadRequest{
		runtimeCommonRequest: runtimeCommon(tc, args.Cwd),
		Path:                 args.Path,
	})
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return formatRuntimeText("read", resp.Content, resp.Truncated, resp.Error), nil
}

type remoteGrepTool struct{}

type remoteGrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
}

func (t *remoteGrepTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentV3ToolGrep,
		Desc: "Search text in the remote runtime workspace.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			agentV3ToolPatternField: {Type: "string", Desc: "Search pattern", Required: true},
			agentV3ToolPathField:    {Type: "string", Desc: "Optional path, default /workspace"},
			agentV3ToolCWDField:     {Type: "string", Desc: agentV3ToolCWDDescription},
		}),
	}, nil
}

func (t *remoteGrepTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, agentV3ToolGrep)
	if err != nil {
		return "", err
	}
	var args remoteGrepArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("grep: invalid arguments: %w", err)
	}
	resp, err := tc.RuntimeClient.Grep(ctx, runtimeGrepRequest{
		runtimeCommonRequest: runtimeCommon(tc, args.Cwd),
		Pattern:              args.Pattern,
		Path:                 args.Path,
	})
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}
	return formatRuntimeText("grep", resp.Output, resp.Truncated, resp.Error), nil
}

type remoteWriteTool struct{}

type remoteWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Cwd     string `json:"cwd,omitempty"`
}

func (t *remoteWriteTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentV3ToolWrite,
		Desc: "Write a file in the remote runtime workspace.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			agentV3ToolPathField:    {Type: "string", Desc: "Workspace file path", Required: true},
			agentV3ToolContentField: {Type: "string", Desc: "Full file content", Required: true},
			agentV3ToolCWDField:     {Type: "string", Desc: agentV3ToolCWDDescription},
		}),
	}, nil
}

func (t *remoteWriteTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, agentV3ToolWrite)
	if err != nil {
		return "", err
	}
	var args remoteWriteArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("write: invalid arguments: %w", err)
	}
	resp, err := tc.RuntimeClient.Write(ctx, runtimeWriteRequest{
		runtimeCommonRequest: runtimeCommon(tc, args.Cwd),
		Path:                 args.Path,
		Content:              args.Content,
	})
	if err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if resp.Error != "" {
		return "[Runtime Error] " + resp.Error, nil
	}
	return fmt.Sprintf("write ok: %d bytes", resp.Bytes), nil
}

type remoteEditTool struct{}

type remoteEditArgs struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
	Cwd   string `json:"cwd,omitempty"`
}

func (t *remoteEditTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentV3ToolEdit,
		Desc: "Apply a unified diff patch to a workspace file in the remote runtime.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			agentV3ToolPathField:  {Type: "string", Desc: "Workspace file path", Required: true},
			agentV3ToolPatchField: {Type: "string", Desc: "Unified diff patch", Required: true},
			agentV3ToolCWDField:   {Type: "string", Desc: agentV3ToolCWDDescription},
		}),
	}, nil
}

func (t *remoteEditTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, agentV3ToolEdit)
	if err != nil {
		return "", err
	}
	var args remoteEditArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("edit: invalid arguments: %w", err)
	}
	resp, err := tc.RuntimeClient.Edit(ctx, runtimeEditRequest{
		runtimeCommonRequest: runtimeCommon(tc, args.Cwd),
		Path:                 args.Path,
		Patch:                args.Patch,
	})
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	if resp.Error != "" {
		return "[Runtime Error] " + resp.Error, nil
	}
	return "edit ok", nil
}

type remoteBashTool struct{}

type remoteBashArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

func (t *remoteBashTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentV3ToolBash,
		Desc: "Run a shell command in the remote runtime workspace. Common utilities include curl, jq, git, tar, gzip, unzip, file, sed, grep, find, and coreutils.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			agentV3ToolCommandField: {Type: "string", Desc: "Shell command to execute with the runtime's installed CLI tools such as curl and jq", Required: true},
			agentV3ToolCWDField:     {Type: "string", Desc: agentV3ToolCWDDescription},
			agentV3ToolTimeoutField: {Type: "string", Desc: "Optional timeout such as 30s, capped by bot config"},
		}),
	}, nil
}

func (t *remoteBashTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, agentV3ToolBash)
	if err != nil {
		return "", err
	}
	var args remoteBashArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("bash: invalid arguments: %w", err)
	}
	resp, err := tc.RuntimeClient.Bash(ctx, runtimeBashRequest{
		runtimeCommonRequest: runtimeCommon(tc, args.Cwd),
		Command:              args.Command,
		Timeout:              args.Timeout,
	})
	if tc.V3 != nil && tc.V3.Trace != nil {
		tc.V3.Trace.RecordBash(resp.ExitCode, resp.DurationMS, resp.Truncated)
	}
	if err != nil {
		return "", fmt.Errorf("bash: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\nduration_ms: %d\ntruncated: %t\n", resp.ExitCode, resp.DurationMS, resp.Truncated)
	if resp.Stdout != "" {
		b.WriteString("stdout:\n")
		b.WriteString(resp.Stdout)
		b.WriteByte('\n')
	}
	if resp.Stderr != "" {
		b.WriteString("stderr:\n")
		b.WriteString(resp.Stderr)
		b.WriteByte('\n')
	}
	if resp.Error != "" {
		b.WriteString("runtime_error: ")
		b.WriteString(resp.Error)
	}
	return strings.TrimSpace(b.String()), nil
}

type loadSkillTool struct{}

type loadSkillArgs struct {
	Name string `json:"name"`
}

func (t *loadSkillTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentV3ToolLoadSkill,
		Desc: "Load one built-in agent-v3 skill by name. Use name=\"rich-message\" as the LAST tool call immediately before the final <telegram_rich_message> answer. If you call any other tool after load_skill, you must call load_skill again before rich output.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			agentV3ToolSkillNameField: {Type: "string", Desc: "Built-in skill name. For Telegram rich output use exactly rich-message, then immediately produce the final rich envelope.", Required: true},
		}),
	}, nil
}

func (t *loadSkillTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("load_skill: %w", errNoTurnContext)
	}
	var args loadSkillArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("load_skill: invalid arguments: %w", err)
	}
	skill, ok := agentV3BuiltinSkillByName(args.Name, tc)
	if !ok {
		return fmt.Sprintf("[Skill Error] Skill %q is not available in this chat.", strings.TrimSpace(args.Name)), nil
	}
	var b strings.Builder
	b.WriteString("<loaded_skill name=\"")
	b.WriteString(escapeAgentV3SkillAttr(skill.Name))
	b.WriteString("\">\n")
	b.WriteString(skill.Content)
	b.WriteString("\n</loaded_skill>\n")
	b.WriteString("ACTIVATION RULE: This skill is active only for your very next assistant response. That response must be the final <telegram_rich_message> envelope. Do not call any other tool first. Do not output normal prose first. If you need another tool, use it now and then call load_skill(name=\"rich-message\") again immediately before rich output.")
	return b.String(), nil
}

func requireAgentV3Runtime(ctx context.Context, name string) (*TurnContext, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return nil, fmt.Errorf("%s: %w", name, errNoTurnContext)
	}
	if tc.RuntimeClient == nil {
		return nil, fmt.Errorf("%s: %w", name, errRuntimeClientUnspecified)
	}
	return tc, nil
}

func runtimeCommon(tc *TurnContext, cwd string) runtimeCommonRequest {
	if cwd == "" {
		cwd = agentV3WorkspaceRootDefault
	}
	return runtimeCommonRequest{
		Namespace: tc.Namespace,
		RunID:     tc.RunID,
		Cwd:       cwd,
	}
}

func formatRuntimeText(op, content string, truncated bool, runtimeErr string) string {
	if runtimeErr != "" {
		return "[Runtime Error] " + runtimeErr
	}
	if content == "" {
		content = op + " returned no content."
	}
	if truncated {
		return content + "\n[truncated]"
	}
	return content
}
