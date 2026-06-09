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

var errRuntimeEndpointEmpty = errors.New("agent v3 runtime endpoint is empty")

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

func (c *RemoteRuntimeClient) Read(ctx context.Context, req runtimeReadRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/read", req, &out)
	out.Content, out.Truncated = truncateForModel(out.Content, c.MaxOutputChars, out.Truncated)
	return out, err
}

func (c *RemoteRuntimeClient) Grep(ctx context.Context, req runtimeGrepRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/grep", req, &out)
	if out.Output == "" {
		out.Output = out.Content
	}
	out.Output, out.Truncated = truncateForModel(out.Output, c.MaxOutputChars, out.Truncated)
	return out, err
}

func (c *RemoteRuntimeClient) Write(ctx context.Context, req runtimeWriteRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/write", req, &out)
	return out, err
}

func (c *RemoteRuntimeClient) Edit(ctx context.Context, req runtimeEditRequest) (runtimeTextResponse, error) {
	var out runtimeTextResponse
	err := c.post(ctx, "/v1/edit", req, &out)
	return out, err
}

func (c *RemoteRuntimeClient) Bash(ctx context.Context, req runtimeBashRequest) (runtimeBashResponse, error) {
	var out runtimeBashResponse
	err := c.post(ctx, "/v1/bash", req, &out)
	out.Stdout, out.Truncated = truncateForModel(out.Stdout, c.MaxOutputChars, out.Truncated)
	out.Stderr, out.Truncated = truncateForModel(out.Stderr, c.MaxOutputChars, out.Truncated)
	return out, err
}

func (c *RemoteRuntimeClient) Status(ctx context.Context) (runtimeStatusResponse, error) {
	var out runtimeStatusResponse
	err := c.get(ctx, "/v1/status", &out)
	return out, err
}

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
		return fmt.Errorf("runtime %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
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
		return fmt.Errorf("runtime %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
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

func buildAgentV3Tools() []tool.BaseTool {
	return []tool.BaseTool{
		&remoteReadTool{},
		&remoteGrepTool{},
		&remoteWriteTool{},
		&remoteEditTool{},
		&remoteBashTool{},
	}
}

func agentV3ToolDefinitionsText() string {
	infos := []map[string]any{
		{"name": "read", "args": "path", "desc": "Read a file from /workspace or /skills."},
		{"name": "grep", "args": "pattern,path?", "desc": "Search literal or regex text in /workspace or /skills."},
		{"name": "write", "args": "path,content", "desc": "Write a file under /workspace."},
		{"name": "edit", "args": "path,patch", "desc": "Apply a unified diff patch to a file under /workspace."},
		{"name": "bash", "args": "command,cwd?,timeout?", "desc": "Run a shell command in the remote runtime namespace."},
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
		Name: "read",
		Desc: "Read a file from the remote runtime. Use this for /workspace files and /skills/*/SKILL.md.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {Type: "string", Desc: "File path, e.g. /skills/name/SKILL.md or /workspace/file.txt", Required: true},
			"cwd":  {Type: "string", Desc: "Optional working directory, default /workspace"},
		}),
	}, nil
}

func (t *remoteReadTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, "read")
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
		Name: "grep",
		Desc: "Search text in the remote runtime. Use grep before reading skills when you need to discover capabilities.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {Type: "string", Desc: "Search pattern", Required: true},
			"path":    {Type: "string", Desc: "Optional path, default /workspace"},
			"cwd":     {Type: "string", Desc: "Optional working directory, default /workspace"},
		}),
	}, nil
}

func (t *remoteGrepTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, "grep")
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
		Name: "write",
		Desc: "Write a file in the remote runtime workspace. Do not write under /skills.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":    {Type: "string", Desc: "Workspace file path", Required: true},
			"content": {Type: "string", Desc: "Full file content", Required: true},
			"cwd":     {Type: "string", Desc: "Optional working directory, default /workspace"},
		}),
	}, nil
}

func (t *remoteWriteTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, "write")
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
		Name: "edit",
		Desc: "Apply a unified diff patch to a workspace file in the remote runtime.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":  {Type: "string", Desc: "Workspace file path", Required: true},
			"patch": {Type: "string", Desc: "Unified diff patch", Required: true},
			"cwd":   {Type: "string", Desc: "Optional working directory, default /workspace"},
		}),
	}, nil
}

func (t *remoteEditTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, "edit")
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
		Name: "bash",
		Desc: "Run a shell command in the remote runtime workspace. Use documented skill CLIs from /skills when available.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {Type: "string", Desc: "Shell command to execute", Required: true},
			"cwd":     {Type: "string", Desc: "Optional working directory, default /workspace"},
			"timeout": {Type: "string", Desc: "Optional timeout such as 30s, capped by bot config"},
		}),
	}, nil
}

func (t *remoteBashTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc, err := requireAgentV3Runtime(ctx, "bash")
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
	b.WriteString(fmt.Sprintf("exit_code: %d\nduration_ms: %d\ntruncated: %t\n", resp.ExitCode, resp.DurationMS, resp.Truncated))
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

func requireAgentV3Runtime(ctx context.Context, name string) (*TurnContext, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return nil, fmt.Errorf("%s: %w", name, errNoTurnContext)
	}
	if tc.RuntimeClient == nil {
		return nil, fmt.Errorf("%s: runtime client is not configured", name)
	}
	return tc, nil
}

func runtimeCommon(tc *TurnContext, cwd string) runtimeCommonRequest {
	if cwd == "" {
		cwd = "/workspace"
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
		content = fmt.Sprintf("%s returned no content.", op)
	}
	if truncated {
		return content + "\n[truncated]"
	}
	return content
}
