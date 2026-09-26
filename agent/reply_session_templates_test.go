package agentv3

import (
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestReplySessionTemplateFields(t *testing.T) {
	tests := []struct {
		name string
		text string
		bad  bool
	}{
		{name: "static", text: "Answer briefly"},
		{name: "metadata", text: "{{.DateTime}} {{.CurrentDateCN}} {{.BotUsername}}"},
		{name: "literal braces", text: `literal .Input and {{"{{context}}"}}`},
		{name: "metadata condition", text: "{{if .BotUsername}}{{.CurrentDateCN}}{{end}}"},
		{name: "metadata alias", text: "{{$date := .DateTime}}{{$date}}"},
		{name: "input", text: "{{.Input}}", bad: true},
		{name: "context messages", text: "{{.ContextMessages}}", bad: true},
		{name: "context text", text: "{{.ContextText}}", bad: true},
		{name: "context xml", text: "{{.ContextXml}}", bad: true},
		{name: "reply xml", text: "{{.ReplyToXml}}", bad: true},
		{name: "dead branch", text: "{{if false}}{{.Input}}{{end}}", bad: true},
		{name: "root alias", text: "{{$root := .}}{{$root.Input}}", bad: true},
		{name: "index", text: `{{index . "Input"}}`, bad: true},
		{name: "call", text: "{{call .Input}}", bad: true},
		{name: "defined template", text: "{{define \"old\"}}{{.ContextXml}}{{end}}{{template \"old\" .}}", bad: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := template.Must(template.New("prompt").Parse(tt.text))
			err := validateReplySessionTemplate(tpl)
			if tt.bad {
				require.ErrorContains(t, err, "reply_chain")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCompileAgentReplySessionTemplateValidationPrecedesModel(t *testing.T) {
	_, err := CompileAgent(t.Context(), &config.AgentConfig{
		Name:           "session",
		ContextMode:    "reply_chain",
		PromptTemplate: config.JoinableString("{{.Input}}"),
	}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session")
	assert.Contains(t, err.Error(), "reply_chain")
	assert.NotContains(t, err.Error(), "model config is nil")
}

func TestCompileAgentSoulOverridesForbiddenReplySystemTemplate(t *testing.T) {
	old := config.BotConfig
	t.Cleanup(func() { config.BotConfig = old })

	soulPath := filepath.Join(t.TempDir(), "soul.md")
	require.NoError(t, os.WriteFile(soulPath, []byte("static soul"), 0o600))
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{SoulPath: soulPath}}

	cc, err := CompileAgent(t.Context(), &config.AgentConfig{
		Name:           "session",
		ContextMode:    "reply_chain",
		SystemPrompt:   config.JoinableString("{{.Input}}"),
		Model:          &config.Model{BaseUrl: "http://model.invalid/v1", ApiKey: "test", Model: "fixture"},
		Agent:          &config.AgentOptions{Enable: true},
		PromptTemplate: config.JoinableString("date={{.CurrentDateCN}}"),
	}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, cc)
}

func TestRenderAgentV3SoulReplySessionRendersMetadata(t *testing.T) {
	old := config.BotConfig
	t.Cleanup(func() { config.BotConfig = old })
	config.BotConfig = nil

	cc := &CompiledAgent{
		Config: &config.AgentConfig{ContextMode: "reply_chain"},
		SystemTemplate: template.Must(template.New("system").Parse(
			"{{.CurrentDateCN}} {{.BotUsername}}",
		)),
	}
	got, err := renderAgentV3Soul(cc, &TurnContext{
		Config:  cc.Config,
		BotUser: &tb.User{Username: "bot"},
	})
	require.NoError(t, err)
	assert.Contains(t, got, "bot")
	assert.Contains(t, got, "年")
}
