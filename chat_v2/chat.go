package chat_v2

import (
	"bytes"
	"context"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

var clients map[string]*openai.Client
var templates *xsync.MapOf[string, chatTemplate]

type chatTemplate struct {
	PromptTemplate       *template.Template
	SystemPromptTemplate *template.Template
}

// var templates map[string]*template.Template

// InitAiClients 初始化AI客户端
func InitAiClients(configs []*config.ChatConfigSingle) {
	clients = make(map[string]*openai.Client)
	// templates = make(map[string]*template.Template)
	templates = xsync.NewMapOf[string, chatTemplate](xsync.WithPresize(len(configs)))

	for _, c := range configs {
		// 初始化模板
		if _, ok := templates.Load(c.Name); !ok {
			var sysPrompt *template.Template
			if c.SystemPrompt != "" {
				sysPrompt = template.Must(template.New("systemPrompt").Parse(c.SystemPrompt))
			}
			templates.Store(c.Name, chatTemplate{
				PromptTemplate:       template.Must(template.New("prompt").Parse(c.PromptTemplate)),
				SystemPromptTemplate: sysPrompt,
			})
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

// 使用template处理prompt模板
type promptData struct {
	DateTime        string
	Input           string
	ContextMessages []*ContextMessage
	ContextText     string
	ContextXml      string
}

var errPromptTemplateNotFound = errors.New("prompt template not found")

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

	contextMsgs, err := GetMessageContext(ctx.Bot(), ctx.Message(), v2.MessageContext)
	if err != nil {
		zap.L().Warn("[Chat] Failed to get message context", zap.Error(err))
	}

	// 准备模板数据
	data := promptData{
		DateTime:        time.Now().Format(time.RFC3339),
		Input:           input,
		ContextMessages: contextMsgs,
		ContextText:     FormatContextMessages(contextMsgs),
		ContextXml:      FormatContextMessagesWithXml(contextMsgs),
	}
	templs, ok := templates.Load(v2.Name)
	if !ok {
		log.Error("Chat prompt template not found", zap.String("name", v2.Name))
		return errPromptTemplateNotFound
	}

	var promptBuf bytes.Buffer
	systemPrompt := v2.SystemPrompt

	if templs.SystemPromptTemplate != nil {
		if err := templs.SystemPromptTemplate.Execute(&promptBuf, data); err != nil {
			return err
		}
		systemPrompt = promptBuf.String()
		promptBuf.Reset()
	}

	messages := make([]openai.ChatCompletionMessage, 0)
	if systemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}

	if err := templs.PromptTemplate.Execute(&promptBuf, data); err != nil {
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
		log.Debug("Chat response", zap.String("response", response))

		// 发送回复
		var replyMsg *tb.Message
		replyMsg, err = ctx.Bot().Reply(ctx.Message(), response, tb.ModeMarkdownV2)
		if err != nil {
			log.Error("Failed to send reply", zap.Error(err))
			return err
		}
		err = orm.PushMessageToStream(replyMsg)
		if err != nil {
			log.Warn("Store bot's reply message to Redis failed", zap.Error(err))
		}
		return err
	}

	return nil
}
