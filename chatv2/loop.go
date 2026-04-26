//go:build !386 && !arm

package chatv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// CustomAgent is a hand-rolled tool-calling loop replacing eino react.Agent:
// it streams every turn (not only the final), refuses tool calls on the final
// step, detects duplicate tool calls and sanitises history before each model call.
type CustomAgent struct {
	name         string
	boundModel   model.ToolCallingChatModel
	invokables   map[string]tool.InvokableTool
	toolNames    []string
	maxSteps     int
	dupThreshold int
}

// CustomAgentConfig configures a CustomAgent.
type CustomAgentConfig struct {
	Name     string
	Model    model.ToolCallingChatModel
	Tools    []tool.BaseTool
	MaxSteps int
}

// NewCustomAgent builds a CustomAgent.
func NewCustomAgent(ctx context.Context, cfg *CustomAgentConfig) (*CustomAgent, error) {
	if cfg == nil {
		return nil, errAgentConfigNil
	}
	if cfg.Model == nil {
		return nil, errModelConfigNil
	}
	if cfg.MaxSteps <= 0 {
		return nil, fmt.Errorf("custom agent %q: %w", cfg.Name, errMaxStepsInvalid)
	}

	infos := make([]*schema.ToolInfo, 0, len(cfg.Tools))
	invokables := make(map[string]tool.InvokableTool, len(cfg.Tools))
	names := make([]string, 0, len(cfg.Tools))

	for _, t := range cfg.Tools {
		info, err := t.Info(ctx)
		if err != nil {
			zap.L().Warn("chatv2/loop: failed to fetch tool info, skipping",
				zap.String("agent", cfg.Name), zap.Error(err))
			continue
		}
		infos = append(infos, info)
		names = append(names, info.Name)

		if it, ok := t.(tool.InvokableTool); ok {
			invokables[info.Name] = it
		} else {
			zap.L().Warn("chatv2/loop: tool is not invokable",
				zap.String("agent", cfg.Name), zap.String("tool", info.Name))
		}
	}

	bound := cfg.Model
	if len(infos) > 0 {
		var err error
		bound, err = cfg.Model.WithTools(infos)
		if err != nil {
			return nil, fmt.Errorf("custom agent %q: bind tools: %w", cfg.Name, err)
		}
	}

	return &CustomAgent{
		name:         cfg.Name,
		boundModel:   bound,
		invokables:   invokables,
		toolNames:    names,
		maxSteps:     cfg.MaxSteps,
		dupThreshold: 3,
	}, nil
}

// Stream runs the agent loop, forwarding chunks from every turn to the reader.
func (a *CustomAgent) Stream(ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](32)
	go a.runLoop(ctx, input, sw)
	return sr, nil
}

// Generate runs the loop and returns the concatenated assistant message.
func (a *CustomAgent) Generate(ctx context.Context, input []*schema.Message) (*schema.Message, error) {
	sr, err := a.Stream(ctx, input)
	if err != nil {
		return nil, err
	}
	defer sr.Close()

	var chunks []*schema.Message
	for {
		chunk, recvErr := sr.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if chunk.Role != schema.Assistant {
			continue
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return &schema.Message{Role: schema.Assistant}, nil
	}
	return schema.ConcatMessages(chunks)
}

func (a *CustomAgent) runLoop(ctx context.Context, input []*schema.Message, sw *schema.StreamWriter[*schema.Message]) {
	defer sw.Close()

	history := append([]*schema.Message{}, input...)
	history = sanitizeHistory(history)
	if len(a.invokables) > 0 {
		history = injectLoopDirectives(history)
	}

	dupCounts := map[string]int{}
	dupWarnInjected := false

	for round := range a.maxSteps {
		if err := ctx.Err(); err != nil {
			sw.Send(nil, err)
			return
		}

		isFinal := round == a.maxSteps-1
		guidanceText := a.computeGuidanceText(history, isFinal, dupWarnInjected)
		turnInput := mergeGuidanceIntoSystem(history, guidanceText)

		assistantMsg, reasoningChunks, sendErr := a.streamOneTurn(ctx, a.boundModel, turnInput, sw)
		if sendErr != nil {
			sw.Send(nil, sendErr)
			return
		}
		if assistantMsg == nil {
			zap.L().Warn("chatv2/loop: empty model response, ending loop",
				zap.String("agent", a.name), zap.Int("round", round))
			return
		}

		if len(assistantMsg.ToolCalls) == 0 {
			for _, rc := range reasoningChunks {
				if closed := sw.Send(rc, nil); closed {
					return
				}
			}
			return
		}

		if closed := sw.Send(newClearStreamOutputMessage(), nil); closed {
			return
		}

		if isFinal {
			sw.Send(schema.AssistantMessage(
				"\n\n（已达到本轮工具调用上限，剩余请求未执行。可换种问法或拆分任务再试。）",
				nil,
			), nil)
			return
		}

		if marker := buildStageMarker(assistantMsg.ToolCalls); marker != "" {
			updateProgressMessage(ctx, marker, wholeTextTypeCollapse)
		}

		history = append(history, assistantMsg)
		sawNewDup := false
		for _, tc := range assistantMsg.ToolCalls {
			key := dupKey(tc.Function.Name, tc.Function.Arguments)
			dupCounts[key]++
			if dupCounts[key] >= a.dupThreshold {
				sawNewDup = true
			}
		}
		dupWarnInjected = sawNewDup

		for _, tc := range assistantMsg.ToolCalls {
			toolMsg := a.executeToolCall(ctx, tc)
			history = append(history, toolMsg)
		}
	}
}

func (a *CustomAgent) streamOneTurn(
	ctx context.Context,
	mdl model.BaseChatModel,
	input []*schema.Message,
	sw *schema.StreamWriter[*schema.Message],
) (*schema.Message, []*schema.Message, error) {
	stream, err := mdl.Stream(ctx, input)
	if err != nil {
		return nil, nil, fmt.Errorf("model stream: %w", err)
	}
	defer stream.Close()

	var chunks []*schema.Message
	var reasoningChunks []*schema.Message
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, nil, fmt.Errorf("model stream recv: %w", recvErr)
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)

		if chunk.Content != "" {
			forward := &schema.Message{
				Role:    chunk.Role,
				Content: chunk.Content,
			}
			if closed := sw.Send(forward, nil); closed {
				return nil, nil, errDownstreamClosed
			}
		}

		if chunk.ReasoningContent != "" {
			reasoningChunks = append(reasoningChunks, &schema.Message{
				Role:             chunk.Role,
				ReasoningContent: chunk.ReasoningContent,
			})
		}
	}

	if len(chunks) == 0 {
		return nil, nil, nil
	}
	merged, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, nil, fmt.Errorf("concat turn chunks: %w", err)
	}
	return merged, reasoningChunks, nil
}

func buildStageMarker(calls []schema.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	names := make([]string, 0, len(calls))
	seen := map[string]bool{}
	for _, tc := range calls {
		n := tc.Function.Name
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	return "▸ 调用工具: " + strings.Join(names, ", ")
}

func (a *CustomAgent) executeToolCall(ctx context.Context, tc schema.ToolCall) *schema.Message {
	name := tc.Function.Name
	args := tc.Function.Arguments

	t, ok := a.invokables[name]
	if !ok {
		zap.L().Warn("chatv2/loop: model called unknown tool",
			zap.String("agent", a.name),
			zap.String("tool", name),
			zap.Strings("available", a.toolNames),
		)
		return schema.ToolMessage(
			fmt.Sprintf(
				"[Tool Error] Tool %q does not exist. Available tools: %s. Please use one of the available tool names exactly.",
				name, strings.Join(a.toolNames, ", "),
			),
			tc.ID,
			schema.WithToolName(name),
		)
	}

	result, err := t.InvokableRun(ctx, args)
	if err != nil {
		zap.L().Warn("chatv2/loop: tool invocation failed",
			zap.String("agent", a.name),
			zap.String("tool", name),
			zap.Error(err),
		)
		result = fmt.Sprintf(
			"[Tool Error] %s\nPlease try a different approach or adjust parameters.",
			err.Error(),
		)
	}

	return schema.ToolMessage(result, tc.ID, schema.WithToolName(name))
}

func (a *CustomAgent) computeGuidanceText(history []*schema.Message, isFinal, dupWarn bool) string {
	var parts []string

	if dupWarn {
		parts = append(parts, "⚠ 你已经多次用相同的参数调用同一个工具。立刻停止重复调用，根据现有结果直接给出最终回答。")
	}

	if isFinal {
		parts = append(parts, finalTurnGuidance)
	} else {
		level, toolRounds := calcGuidanceLevel(history, a.maxSteps)
		switch level {
		case guidanceNone:
		case guidanceSoft:
			parts = append(parts, fmt.Sprintf(softTurnGuidance, toolRounds))
		case guidanceHard:
			parts = append(parts, finalTurnGuidance)
		}
	}

	return strings.Join(parts, "\n\n")
}

func mergeGuidanceIntoSystem(history []*schema.Message, guidance string) []*schema.Message {
	if guidance == "" {
		out := make([]*schema.Message, len(history))
		copy(out, history)
		return out
	}
	out := make([]*schema.Message, len(history))
	copy(out, history)
	for i, msg := range out {
		if msg != nil && msg.Role == schema.System {
			merged := *msg
			if merged.Content == "" {
				merged.Content = guidance
			} else {
				merged.Content = msg.Content + "\n\n" + guidance
			}
			out[i] = &merged
			return out
		}
	}
	prepended := make([]*schema.Message, 0, len(out)+1)
	prepended = append(prepended, schema.SystemMessage(guidance))
	prepended = append(prepended, out...)
	return prepended
}

func dupKey(name, args string) string {
	canon := canonicalizeJSON(args)
	h := sha256.Sum256([]byte(name + "|" + canon))
	return hex.EncodeToString(h[:8])
}

func canonicalizeJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	out, err := marshalSorted(v)
	if err != nil {
		return s
	}
	return out
}

func marshalSorted(v any) (string, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kj, _ := json.Marshal(k)
			b.Write(kj)
			b.WriteByte(':')
			vs, err := marshalSorted(x[k])
			if err != nil {
				return "", err
			}
			b.WriteString(vs)
		}
		b.WriteByte('}')
		return b.String(), nil
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			vs, err := marshalSorted(item)
			if err != nil {
				return "", err
			}
			b.WriteString(vs)
		}
		b.WriteByte(']')
		return b.String(), nil
	default:
		out, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
}

func sanitizeHistory(in []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(in))

	for i := range in {
		msg := in[i]
		if msg == nil {
			continue
		}

		if msg.Role == schema.Tool {
			if !precededByMatchingToolCall(out, msg) {
				continue
			}
			out = append(out, msg)
			continue
		}

		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			needed := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				needed[tc.ID] = true
			}
			j := i + 1
			for j < len(in) && in[j] != nil && in[j].Role == schema.Tool && needed[in[j].ToolCallID] {
				delete(needed, in[j].ToolCallID)
				j++
			}
			if len(needed) > 0 {
				continue
			}
			out = append(out, msg)
			continue
		}

		out = append(out, msg)
	}
	return out
}

func precededByMatchingToolCall(out []*schema.Message, toolMsg *schema.Message) bool {
	for k := len(out) - 1; k >= 0; k-- {
		prev := out[k]
		if prev == nil {
			return false
		}
		if prev.Role == schema.Tool {
			continue
		}
		if prev.Role != schema.Assistant {
			return false
		}
		for _, tc := range prev.ToolCalls {
			if tc.ID == toolMsg.ToolCallID {
				return true
			}
		}
		return false
	}
	return false
}

const loopDirectiveText = "工具调用纪律：\n" +
	"1. 每一轮回复要么调用工具推进任务，要么直接给出最终答案，二者必择其一。\n" +
	"2. 在决定调用工具之前，用一句话简要说明你接下来要做什么（例如“我先搜索X”“接下来读取Y”），让用户看到进度；这句说明作为正文输出，不要放在思维链里。\n" +
	"3. 一旦已有信息足以回答用户，立即停止工具调用并整理输出。不要为了“更全面”而反复调工具。\n" +
	"4. 严禁用相同的参数重复调用同一个工具；若上一次调用失败或结果不理想，必须改变参数或换一种方式，否则停下并说明原因。\n" +
	"5. 工具结果若返回 [Tool Error] 或 [Tool Error] Tool ... does not exist，说明该路径不可行：换工具或直接基于已有信息作答，禁止原样重试。\n" +
	"6. 若工具已经直接给出了用户想要的内容（例如 update_progress 已写入了最终答复），不要再发起新一轮工具调用，直接结束本次回答。\n" +
	"7. 阶段性报告应简短（一句话），不要长篇大论描述内部步骤；真正的细节放在最终答案里。"

func injectLoopDirectives(history []*schema.Message) []*schema.Message {
	directive := schema.SystemMessage(loopDirectiveText)
	for i, msg := range history {
		if msg != nil && msg.Role == schema.System {
			merged := *msg
			if merged.Content == "" {
				merged.Content = loopDirectiveText
			} else {
				merged.Content = msg.Content + "\n\n" + loopDirectiveText
			}
			out := make([]*schema.Message, len(history))
			copy(out, history)
			out[i] = &merged
			return out
		}
	}
	out := make([]*schema.Message, 0, len(history)+1)
	out = append(out, directive)
	out = append(out, history...)
	return out
}
