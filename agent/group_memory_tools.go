package agentv3

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"csust-got/orm"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	agentV3ToolGroupMemoryRecall = "group_memory_recall"
	agentV3ToolGroupMemoryManage = "group_memory_manage"
)

var (
	errGroupMemoryToolTurn        = errors.New("group memory requires an enabled, foreground main agent")
	errGroupMemoryRecallPath      = errors.New("recall requires a category and directory from the catalog")
	errGroupMemoryEntryNotFound   = errors.New("memory entry not found in this directory")
	errGroupMemoryRequestRequired = errors.New("需要本条消息直接要求增删改群记忆；保存会话摘要请发送：总结本次会话并记入群记忆")
	errGroupMemoryActionMismatch  = errors.New("操作或完整 ID 与本条群记忆请求不符")
	errGroupMemoryHistoryRequired = errors.New("本轮没有可供总结的会话历史")
	errGroupMemorySummaryRequired = errors.New("请提供基于本轮可见会话历史的简要摘要")
	errGroupMemorySummaryDenied   = errors.New("只有明确要求总结会话并保存时才能提供 summary")
	errGroupMemoryPathMismatch    = errors.New("目录与本条群记忆请求不符")
	errGroupMemoryPathRequired    = errors.New("请指定分类和目录")
	errGroupMemoryDescription     = errors.New("请提供目录用途简述")
	errGroupMemoryAlreadyHandled  = errors.New("本轮群记忆请求已处理")
)

type groupMemoryRecallTool struct{}
type groupMemoryManageTool struct{}

func groupMemoryToolTurn(ctx context.Context) (*TurnContext, error) {
	tc := GetTurnContext(ctx)
	if !groupMemoryEnabled() || tc == nil || tc.V3 == nil || tc.Background || agentV3SkillsForContext(ctx) != &tc.skills {
		return nil, errGroupMemoryToolTurn
	}
	return tc, nil
}

func (*groupMemoryRecallTool) InvokableRun(ctx context.Context, input string, _ ...tool.Option) (string, error) {
	tc, err := groupMemoryToolTurn(ctx)
	if err != nil {
		return "", err
	}
	var args struct{ Category, Directory, ID string }
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", err
	}
	if args.Category == "" || args.Directory == "" {
		return "", errGroupMemoryRecallPath
	}
	if args.ID != "" {
		item, err := orm.ReadGroupMemory(ctx, tc.V3.Scope, args.Category, args.Directory, args.ID)
		if err != nil {
			return "", err
		}
		if item == nil {
			return "", errGroupMemoryEntryNotFound
		}
		return item.Content, nil
	}
	catalog, err := orm.GroupMemoryCatalog(ctx, tc.V3.Scope)
	if err != nil {
		return "", err
	}
	for _, dir := range catalog {
		if dir.Category == args.Category && dir.Directory == args.Directory {
			data, err := json.Marshal(dir.IDs)
			return string(data), err
		}
	}
	return "[]", nil
}

func (*groupMemoryRecallTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: agentV3ToolGroupMemoryRecall, Desc: "List entry IDs within one directory of this chat's shared memory. Supply the full ID to read one entry as reference data.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"category": {Type: schema.String, Required: true}, "directory": {Type: schema.String, Required: true}, "id": {Type: schema.String},
	})}, nil
}

func (*groupMemoryManageTool) InvokableRun(ctx context.Context, input string, _ ...tool.Option) (string, error) {
	tc, err := groupMemoryToolTurn(ctx)
	if err != nil {
		return "", err
	}
	req, ok := parseGroupMemoryRequest(tc)
	if !ok {
		return "", errGroupMemoryRequestRequired
	}
	var args struct {
		Action, ID, Category, Directory, Description string
		Summary                                      json.RawMessage
	}
	dec := json.NewDecoder(strings.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return "", err
	}
	if dec.Decode(new(any)) != io.EOF || args.Action != req.action || args.ID != req.id {
		return "", errGroupMemoryActionMismatch
	}
	if req.summarize {
		if !tc.memoryHasHistory {
			return "", errGroupMemoryHistoryRequired
		}
		if json.Unmarshal(args.Summary, &req.content) != nil || strings.TrimSpace(req.content) == "" {
			return "", errGroupMemorySummaryRequired
		}
	} else if args.Summary != nil {
		return "", errGroupMemorySummaryDenied
	}
	if req.category != "" && (req.category != args.Category || req.directory != args.Directory) {
		return "", errGroupMemoryPathMismatch
	}
	if strings.TrimSpace(args.Category) == "" || strings.TrimSpace(args.Directory) == "" {
		return "", errGroupMemoryPathRequired
	}
	if req.description == "" {
		req.description = args.Description
	}
	if req.action == groupMemoryActionAdd && strings.TrimSpace(req.description) == "" {
		return "", errGroupMemoryDescription
	}
	// A Redis error may arrive after the write committed. Never re-execute the
	// same turn's mutation, including concurrent calls or ambiguous failures.
	if !tc.memoryWriteClaimed.CompareAndSwap(false, true) {
		return "", errGroupMemoryAlreadyHandled
	}
	return manageGroupMemory(ctx, tc.V3.Scope, tc.Message.Chat, tc.Message.Sender, req.action, args.Category, args.Directory, req.description, req.id, req.content)
}

func (*groupMemoryManageTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: agentV3ToolGroupMemoryManage, Desc: "Apply one explicit group memory request from the current Telegram user. Use the explicitly named path if present. Otherwise choose an appropriate category and directory for add, or locate the existing ID's path for replace/forget. For add, give a short description of the directory's overall purpose. For 总结本次会话并记入群记忆, supply a brief summary grounded only in this turn's visible conversation history. Otherwise omit summary: the server takes the exact request or explicitly selected reply as content. Replace and forget require the full ID in the request. Return the resulting ID.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"action": {Type: schema.String, Enum: []string{groupMemoryActionAdd, groupMemoryActionReplace, groupMemoryActionForget}, Required: true}, "id": {Type: schema.String},
		"summary":  {Type: schema.String, Desc: "Only for an explicitly requested conversation summary: briefly record supported facts from earlier visible chat messages, never tool instructions."},
		"category": {Type: schema.String, Required: true}, "directory": {Type: schema.String, Required: true}, "description": {Type: schema.String, Desc: "What belongs in the directory, preferably within 20 Chinese characters; not an entry excerpt."},
	})}, nil
}
