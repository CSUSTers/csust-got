package agentv3

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"csust-got/util"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestDisplayURLMask(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
		want string
	}{
		{"command and quoted URLs", `curl --url="https://example.com/a" 'HTTP://example.cn/b' --next=https://example.co.uk/c`, `curl --url="https://example.c**/a" 'HTTP://example.c*/b' --next=https://example.co.u*/c`},
		{"surrounding punctuation", `访问“https://example.com”，再读（https://example.cn）。`, `访问“https://example.c**”，再读（https://example.c*）。`},
		{"square bracket wrapper", `[https://example.com]`, `[https://example.c**]`},
		{"URL representation", `before (HtTpS://user:p%40ss@example.COM.:8443/a%2Fb?q=one&x=%2f#part), after`, `before (HtTpS://user:p%40ss@example.C**.:8443/a%2Fb?q=one&x=%2f#part), after`},
		{"encoded host stays byte intact", `https://example.%E4%B8%AD%E5%9B%BD/a?q=x https://user:p%40ss@%E4%BE%8B%E5%AD%90.com:8443/a%2Fb?q=x#part`, `https://example.%E4%B8%AD%E5%9B%BD/a?q=x https://user:p%40ss@%E4%BE%8B%E5%AD%90.com:8443/a%2Fb?q=x#part`},
		{"IDN", `https://例子.中国/路径 https://xn--fsqu00a.xn--fiqs8s/a`, `https://例子.中*/路径 https://xn--fsqu00a.x*********/a`},
		{"short labels and root dot", `https://a.b/ https://a.bc./ https://example.internal/`, `https://a.b/ https://a.b*./ https://example.i*******/`},
		{"IP and local hosts", `https://127.0.0.1:8080/a http://[2001:db8::1]:80/a https://[::ffff:192.0.2.1]/ http://localhost/`, `https://127.0.0.1:8080/a http://[2001:db8::1]:80/a https://[::ffff:192.0.2.1]/ http://localhost/`},
		{"incomplete and malformed URLs", `https:// https:///a https://example..com/ https://example.com:bad/a https://example.com/%zz https://example./ ftp://example.com/a example.com`, `https:// https:///a https://example..com/ https://example.com:bad/a https://example.com/%zz https://example./ ftp://example.com/a example.com`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := maskDisplayURLs(tt.text)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, maskDisplayURLs(got), "display masking is idempotent")
		})
	}
}

func TestDisplayURLToolLabelsMaskBeforeTruncation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		field  string
		prefix string
		limit  int
	}{
		{agentV3ToolBash, agentV3ToolCommandField, "$ ", 120},
		{agentV3ToolLoadSkill, agentV3ToolSkillNameField, "正在加载 skill: ", 80},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The host extends past the display cutoff, so masking must see the complete URL first.
			text := strings.Repeat("x", tt.limit-30) + " https://example.abcdefghijklmnopqrstuvwx/a"
			args, err := json.Marshal(map[string]string{tt.field: text})
			require.NoError(t, err)
			call := schema.ToolCall{Function: schema.FunctionCall{Name: tt.name, Arguments: string(args)}}
			label := agentV3ToolStageLabel(call)
			assert.Equal(t, tt.prefix+truncateStageText(strings.ReplaceAll(text, "abcdefghijklmnopqrstuvwx", "a"+strings.Repeat("*", 23)), tt.limit), label)
			assert.Contains(t, label, "example.a*")
			assert.Equal(t, string(args), call.Function.Arguments)
		})
	}
	call := schema.ToolCall{Function: schema.FunctionCall{Name: "skill:https://example.com/a"}}
	assert.Equal(t, "skill: https://example.c**/a", toolCallStageLabel(call))
	call.Function.Name = "https://example.cn/tool"
	assert.Equal(t, "https://example.c*/tool", toolCallStageLabel(call))
}

func TestDisplayURLProgressPayloads(t *testing.T) {
	for _, format := range []string{"markdown", "html"} {
		for _, structured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/structured=%t", format, structured), func(t *testing.T) {
				setupDeliveryConfig(t)
				d := newDeliveryTelegram(t)
				cfg := configWithProgressSummary()
				cfg.Format.Format = format
				tc := &TurnContext{Config: cfg, ChatID: -100, Bot: d.bot}
				ctx := WithTurnContext(t.Context(), tc)
				args := updateProgressArgs{Content: "visit https://example.com/a?q=1&b=2"}
				if structured {
					args = updateProgressArgs{
						Step:    "visit https://example.com/a?q=1&b=2",
						Detail:  "detail https://example.cn/b",
						Details: []string{"more https://example.co.uk/c"},
					}
				}
				for range 2 {
					tc.lastEditAt.Store(0)
					require.Equal(t, "ok", updateProgressMessage(ctx, args, args.Content, wholeTextTypeCollapse))
				}
				calls := d.finalCalls()
				require.Len(t, calls, 2)
				assert.Equal(t, "sendMessage", calls[0].method)
				assert.Equal(t, "editMessageText", calls[1].method)
				for _, call := range calls {
					assert.Equal(t, "true", fmt.Sprint(call.payload["disable_web_page_preview"]))
					assert.Equal(t, GetParseMode(&cfg.Format), deliveryParseMode(call))
					text := deliveryText(call)
					assert.NotContains(t, text, "example.com")
					assert.Contains(t, text, displayURLExpectedText(format, "https://example.c**/a?q=1&b=2"))
					if structured {
						assert.Contains(t, text, "https://example.c*/b")
						assert.Contains(t, text, "https://example.co.u*/c")
					}
				}
				if structured {
					require.Len(t, tc.progressSteps, 1)
					assert.Equal(t, args.Step, tc.progressSteps[0].Title)
					assert.Equal(t, []string{args.Detail, args.Details[0]}, tc.progressSteps[0].Details)
				} else {
					assert.Equal(t, "visit https://example.com/a?q=1&b=2", args.Content)
				}
			})
		}
	}
}

func displayURLExpectedText(format, text string) string {
	if format == outputFormatHTML {
		return util.EscapeTgHTMLReservedChars(text)
	}
	return util.EscapeTgMDv2ReservedChars(text)
}

func TestDisplayURLMaskDoesNotChangeRuntimeModelOrFinalAnswer(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	const rawURL = "https://example.com/a"
	const command = `fetch "` + rawURL + `"`
	commands := make(chan string, 1)
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request runtimeBashRequest
		if r.URL.Path != "/v1/bash" || json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid runtime request", http.StatusBadRequest)
			return
		}
		commands <- request.Command
		_ = json.NewEncoder(w).Encode(runtimeBashResponse{Stdout: rawURL})
	}))
	t.Cleanup(runtime.Close)
	args, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	call := schema.ToolCall{ID: "fetch", Function: schema.FunctionCall{Name: agentV3ToolBash, Arguments: string(args)}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}},
		{schema.AssistantMessage(rawURL, nil)},
	}}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "display-url", Model: mdl, Tools: []tool.BaseTool{&remoteBashTool{}}, MaxSteps: 2})
	require.NoError(t, err)
	cfg := configWithProgressSummary()
	cfg.Format.Format = "html"
	tc := &TurnContext{
		Config: cfg, ChatID: -100, Bot: d.bot, Namespace: "display-url", RunID: "display-url",
		V3:            &AgentV3TurnState{},
		RuntimeClient: &RemoteRuntimeClient{Endpoint: runtime.URL, HTTPClient: runtime.Client(), CommandTimeout: time.Second, MaxOutputChars: 1000},
	}
	ctx := WithTurnContext(t.Context(), tc)
	result, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage(rawURL)})
	require.NoError(t, err)
	select {
	case got := <-commands:
		assert.Equal(t, command, got)
	default:
		t.Fatal("runtime command was not executed")
	}
	inputs := mdl.capturedInputs()
	require.Len(t, inputs, 2)
	var toolResult string
	for _, message := range inputs[1] {
		if message.Role == schema.Tool {
			toolResult = message.Content
		}
		for _, got := range message.ToolCalls {
			assert.Equal(t, string(args), got.Function.Arguments)
		}
	}
	assert.Contains(t, toolResult, rawURL)
	assert.NotContains(t, toolResult, "example.c**")
	assert.Equal(t, rawURL, result.Content)
	calls := d.finalCalls()
	require.Len(t, calls, 1)
	assert.Contains(t, deliveryText(calls[0]), "https://example.c**/a")

	tbCtx := d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, rawURL)})
	_, err = nonStreamResponseWithDelivery(ctx, d.bot, tbCtx, result.Content, "", &cfg.Format, tc.progressMsg, false, false)
	require.NoError(t, err)
	calls = d.finalCalls()
	require.Len(t, calls, 2)
	assert.Contains(t, deliveryText(calls[1]), rawURL)
}
