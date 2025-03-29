package chat_v2

import (
	"bytes"
	"context"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/util"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
	"net/http"
	"net/url"
	"strings"
	"text/template"
)

var clients map[string]*openai.Client
var templates map[string]*template.Template

// InitAiClients 初始化AI客户端
func InitAiClients(configs []*config.ChatConfigSingle) {
	clients = make(map[string]*openai.Client)
	templates = make(map[string]*template.Template)

	for _, c := range configs {
		// 初始化模板
		if _, ok := templates[c.Name]; !ok {
			templates[c.Name] = template.Must(template.New(c.Name).Parse(c.PromptTemplate))
		}

		if _, ok := clients[c.Model.Name]; ok {
			continue
		}

		clientConfig := openai.DefaultConfig(c.Model.ApiKey)
		clientConfig.BaseURL = c.Model.BaseUrl

		if c.Model.Proxy != "" {
			proxyURL, err := url.Parse(c.Model.Proxy)
			if err != nil {
				zap.L().Fatal("failed to parse proxy URL", zap.Error(err))
			}
			httpClient := &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(proxyURL),
				},
			}
			clientConfig.HTTPClient = httpClient
		}

		client := openai.NewClientWithConfig(clientConfig)
		clients[c.Model.Name] = client
	}
}

// Chat 处理聊天请求
func Chat(ctx tb.Context, v2 *config.ChatConfigSingle, trigger *config.ChatTrigger) error {

	input := ctx.Message().Text
	if input == "" {
		input = ctx.Message().Caption
	}
	if trigger.Command != "" {
		_, text, err := entities.CommandFromText(input, 0)
		if err != nil {
			input = text
		}
	}

	messages := make([]openai.ChatCompletionMessage, 0)
	if v2.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: v2.SystemPrompt,
		})
	}

	// 使用template处理prompt模板
	type PromptData struct {
		Input   string
		Context string
	}

	contextMsgs, err := GetMessageContext(ctx.Bot(), ctx.Message(), v2.MessageContext)
	if err != nil {
		zap.L().Warn("[Chat] Failed to get message context", zap.Error(err))
	}

	// 准备模板数据
	data := PromptData{
		Input:   input,
		Context: FormatContextMessages(contextMsgs),
	}

	var promptBuf bytes.Buffer
	if err := templates[v2.Name].Execute(&promptBuf, data); err != nil {
		return err
	}

	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: promptBuf.String(),
	})

	zap.L().Debug("Chat context messages", zap.Any("messages", messages))

	client := clients[v2.Model.Name]
	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:       v2.Model.Model,
		Messages:    messages,
		Temperature: v2.GetTemperature(),
	})

	if err != nil {
		return err
	}

	// 获取AI响应并发送回复
	if len(resp.Choices) > 0 {
		response := resp.Choices[0].Message.Content
		// 移除可能的空行
		response = strings.TrimSpace(response)
		response = util.EscapeTelegramReservedChars(response)

		// 发送回复
		_, err = ctx.Bot().Reply(ctx.Message(), response, tb.ModeMarkdownV2)
		return err
	}

	return nil
}
