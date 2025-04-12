package chat

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

var (
	client   *openai.Client
	chatChan = make(chan *chatContext, 16)
)

type chatContext struct {
	Context
	req *openai.ChatCompletionRequest
	msg *Message
}

// ChatInfo is input of ChatWith
// nolint: revive
type ChatInfo struct {
	Setting

	Text string
}

// Setting is settings of ChatWith
type Setting struct {
	Stream bool
	Reply  bool

	Placeholder string

	Model        string
	SystemPrompt string

	Temperature float32
}

// GetPlaceholder get message placeholder
func (s *Setting) GetPlaceholder() string {
	if s.Placeholder == "" {
		return "正在思考..."
	}
	return s.Placeholder
}

// InitChat init chat service
func InitChat() {
	if config.BotConfig.ChatConfig.Key != "" {
		clientConfig := openai.DefaultConfig(config.BotConfig.ChatConfig.Key)
		if u, err := url.Parse(config.BotConfig.Proxy); err == nil && u.Host != "" {
			clientConfig.HTTPClient = &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(u),
				},
			}
		} else {
			log.Error("[chat] failed to parse proxy url", zap.Error(err))
		}

		if baseApiUrl, err := url.Parse(config.BotConfig.ChatConfig.BaseUrl); err == nil && baseApiUrl.Host != "" {
			clientConfig.BaseURL = baseApiUrl.String()
		} else {
			log.Error("[chat] failed to set custom api url", zap.Error(err))
		}

		client = openai.NewClientWithConfig(clientConfig)
		go chatService()
	}
}

// ChatWith chat with GPT
// nolint: revive
func ChatWith(ctx Context, info *ChatInfo) error {
	if client == nil {
		return nil
	}

	req, err := generateRequest(ctx, info)
	if err != nil {
		return err
	}

	var msg *Message
	if info.Reply {
		msg, err = util.SendReplyWithError(ctx.Chat(), info.GetPlaceholder(), ctx.Message())
	} else {
		msg, err = util.SendMessageWithError(ctx.Chat(), info.GetPlaceholder(), ctx.Message())
	}
	if err != nil {
		return err
	}

	payload := &chatContext{Context: ctx, req: req, msg: msg}

	select {
	case chatChan <- payload:
		return nil
	default:
		return ctx.Reply("要处理的对话太多了，要不您稍后再试试？")
	}
}

func generateRequest(ctx Context, info *ChatInfo) (*openai.ChatCompletionRequest, error) {
	chatCfg := config.BotConfig.ChatConfig
	req := openai.ChatCompletionRequest{
		Model:       openai.GPT3Dot5Turbo,
		MaxTokens:   chatCfg.MaxTokens,
		Messages:    []openai.ChatCompletionMessage{},
		Stream:      info.Stream,
		Temperature: chatCfg.Temperature,
	}

	if info.Temperature > 0 {
		req.Temperature = info.Temperature
	}

	if info.Model != "" {
		req.Model = info.Model
	} else if chatCfg.Model != "" {
		req.Model = chatCfg.Model
	}

	prompt := info.SystemPrompt
	if len(req.Messages) == 0 && chatCfg.SystemPrompt != "" && prompt == "" {
		prompt = chatCfg.SystemPrompt
	}

	if prompt != "" {
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: prompt,
		})
	}

	keepContext := chatCfg.KeepContext
	if keepContext > 0 && ctx.Message().ReplyTo != nil {
		chatContext, err := orm.GetChatContext(ctx.Chat().ID, ctx.Message().ReplyTo.ID)
		if err == nil {
			if len(chatContext) > 2*keepContext {
				chatContext = chatContext[len(chatContext)-2*keepContext:]
			}
			req.Messages = append(req.Messages, chatContext...)
		}
	}

	req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: info.Text})

	return &req, nil
}

func chatService() {
	for ctx := range chatChan {
		go func(ctx *chatContext) {
			defer func() {
				if err := recover(); err != nil {
					log.Error("[ChatGPT] Panic", zap.Any("err", err))
				}
			}()

			if ctx.req.Stream {
				chatWithStream(ctx)
			} else {
				chatWithoutStream(ctx)
			}

		}(ctx)
	}
}

func extractStatusCode(err error) int {
	// 从错误消息中提取状态码
	re := regexp.MustCompile(`status code: (\d+)`)
	matches := re.FindStringSubmatch(err.Error())
	if len(matches) > 1 {
		statusCode, err := strconv.Atoi(matches[1])
		if err == nil {
			return statusCode
		}
	}
	return 0
}

func handleStreamError(ctx *chatContext, err error) bool {
	log.Error("[ChatGPT] Error", zap.Error(err))
	statusCode := extractStatusCode(err)
	if statusCode == 429 { // 错误代码为429
		log.Debug("[ChatGPT] Rate limit exceeded, retrying...", zap.Error(err))
		return true // 重试
	}
	_, err = util.EditMessageWithError(ctx.msg,
		"An error occurred. If this issue persists please contact us through our help center at help.openai.com.")
	if err != nil {
		log.Error("[ChatGPT] Can't edit message", zap.Error(err))
	}
	return false // 不重试

}

func chatWithStream(ctx *chatContext) {
	start := time.Now()

	retryNums := config.BotConfig.ChatConfig.RetryNums
	retryInterval := config.BotConfig.ChatConfig.RetryInterval

	var replyMsg *Message
	var stream *openai.ChatCompletionStream
	var err error

	// 重试5次，每次间隔1s
	for i := range retryNums {
		log.Debug("[ChatGPT] retry", zap.Int("retry", i), zap.String("content", ctx.req.Messages[len(ctx.req.Messages)-1].Content))
		stream, err = client.CreateChatCompletionStream(context.Background(), *ctx.req)
		if err == nil {
			log.Debug("[ChatGPT] Create stream successfully", zap.Duration("duration", time.Since(start)))
			break // 如果成功创建stream，跳出循环
		}
		if handleStreamError(ctx, err) {
			time.Sleep(time.Duration(retryInterval) * time.Second) // retryInterval 秒后重试
			continue
		}
		return
	}

	defer func(stream *openai.ChatCompletionStream) {
		err = stream.Close()
		if err != nil {
			log.Error("[ChatGPT] Stream close error", zap.Error(err))
		}
	}(stream)

	content := ""
	contentLock := sync.Mutex{}
	done := make(chan struct{})
	go func() {
		for {
			var response openai.ChatCompletionStreamResponse
			response, err = stream.Recv()
			if errors.Is(err, io.EOF) {
				ctx.req.Messages = append(ctx.req.Messages, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				})
				done <- struct{}{}
				break
			}

			if err != nil {
				contentLock.Lock()
				content += "\n\n...寄了"
				contentLock.Unlock()
				log.Error("[ChatGPT] Stream error", zap.Error(err))
				break
			}

			contentLock.Lock()
			content += response.Choices[0].Delta.Content
			contentLock.Unlock()
		}
	}()

	ticker := time.NewTicker(5 * time.Second) // 编辑过快会被tg限流
	defer ticker.Stop()
	lastContent := "" // 记录上次编辑的内容，内容相同则不再编辑，避免tg的api返回400
out:
	for range ticker.C {
		contentLock.Lock()
		contentCopy := content
		contentLock.Unlock()
		if len(strings.TrimSpace(contentCopy)) > 0 && strings.TrimSpace(contentCopy) != strings.TrimSpace(lastContent) {
			replyMsg, err = util.EditMessageWithError(ctx.msg, contentCopy)
			if err != nil {
				log.Error("[ChatGPT] Can't edit message", zap.Error(err))
			} else {
				lastContent = contentCopy
			}
		}
		select {
		case <-done:
			break out
		default:
		}
	}

	contentLock.Lock()
	if strings.TrimSpace(content) == "" {
		content += "\n...嗦不粗话"
	}
	if config.BotConfig.DebugMode {
		content += fmt.Sprintf("\n\ntime cost: %v\n", time.Since(start))
		replyMsg, err = util.EditMessageWithError(ctx.msg, content)
		if err != nil {
			log.Error("[ChatGPT] Can't edit message", zap.Error(err))
		}
	}
	contentLock.Unlock()

	if replyMsg != nil {
		err = orm.SetChatContext(ctx.Context.Chat().ID, replyMsg.ID, ctx.req.Messages)
		if err != nil {
			log.Error("[ChatGPT] Can't set chat context", zap.Error(err))
		}
	}
}
func chatWithoutStream(ctx *chatContext) {
	start := time.Now()

	retryNums := config.BotConfig.ChatConfig.RetryNums
	retryInterval := config.BotConfig.ChatConfig.RetryInterval

	var resp openai.ChatCompletionResponse
	var err error

	for range retryNums {
		resp, err = client.CreateChatCompletion(context.Background(), *ctx.req)
		if err == nil {
			break // 如果成功创建stream，跳出循环
		}
		if handleStreamError(ctx, err) {
			time.Sleep(time.Duration(retryInterval) * time.Second) // retryInterval 秒后重试
			continue
		}
		return
	}

	content := resp.Choices[0].Message.Content

	if strings.TrimSpace(content) == "" {
		content += "\n...嗦不粗话"
	}

	if config.BotConfig.DebugMode {
		content += fmt.Sprintf("\n\nusage: %d + %d = %d tokens\n",
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.TotalTokens)
		content += fmt.Sprintf("time cost: %v\n", time.Since(start))
	}
	replyMsg, err := util.EditMessageWithError(ctx.msg, content)
	if err != nil {
		log.Error("[ChatGPT] Can't edit message", zap.Error(err))
		return
	}

	err = orm.SetChatContext(ctx.Context.Chat().ID, replyMsg.ID, ctx.req.Messages)
	if err != nil {
		log.Error("[ChatGPT] Can't set chat context", zap.Error(err))
	}
}
