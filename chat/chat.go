package chat

import (
	"context"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	gogpt "github.com/sashabaranov/go-gpt3"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

var (
	client   *gogpt.Client
	chatChan = make(chan *chatContext, 16)
)

type chatContext struct {
	Context
	req *gogpt.ChatCompletionRequest
	msg *Message
}

func InitChat() {
	if config.BotConfig.ChatConfig.Key != "" {
		client = gogpt.NewClient(config.BotConfig.ChatConfig.Key)
		go chatService()
	}
}

// Chat with GPT
func ChatGPT(ctx Context) error {
	if client == nil {
		return nil
	}

	command := entities.FromMessage(ctx.Message())
	if command.Argc() <= 0 {
		return ctx.Reply("您好，有什么问题可以为您解答吗？")
	}
	arg := command.ArgAllInOneFrom(0)
	if len(arg) > config.BotConfig.ChatConfig.PromptLimit || len(strings.TrimSpace(arg)) == 0 {
		return ctx.Reply("An error occurred. If this issue persists please contact us through our help center at help.openai.com.")
	}

	req := gogpt.ChatCompletionRequest{
		Model:     gogpt.GPT3Dot5Turbo,
		MaxTokens: config.BotConfig.ChatConfig.MaxTokens,
		Messages: []gogpt.ChatCompletionMessage{
			{Role: "user", Content: arg},
		},
		Stream:      false,
		Temperature: config.BotConfig.ChatConfig.Temperature,
	}

	msg, err := util.SendReplyWithError(ctx.Chat(), "正在思考...", ctx.Message())
	if err != nil {
		return err
	}

	payload := &chatContext{Context: ctx, req: &req, msg: msg}

	select {
	case chatChan <- payload:
		return nil
	default:
		return ctx.Reply("An error occurred. If this issue persists please contact us through our help center at help.openai.com.")
	}
}

func chatService() {
	for ctx := range chatChan {
		go func(ctx *chatContext) {
			start := time.Now()

			// resp, err := client.CreateChatCompletion(context.Background(), *ctx.req)
			// if err != nil {
			// 	log.Error("[ChatGPT] Can't create completion", zap.Error(err))
			// 	return
			// }
			// fmt.Printf("%+v", resp)

			// content := resp.Choices[0].Message.Content

			stream, err := client.CreateChatCompletionStream(context.Background(), *ctx.req)
			if err != nil {
				util.EditMessageWithError(ctx.msg, "An error occurred. If this issue persists please contact us through our help center at help.openai.com.")
				return
			}
			defer stream.Close()

			content := ""
			contentLock := sync.Mutex{}
			done := make(chan struct{})
			go func() {
				for {
					response, err := stream.Recv()
					if errors.Is(err, io.EOF) {
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

		out:
			for range time.Tick(time.Second) {
				contentLock.Lock()
				contentCopy := content
				contentLock.Unlock()
				if len(strings.TrimSpace(contentCopy)) > 0 {
					util.EditMessageWithError(ctx.msg, contentCopy)
				}
				select {
				case <-done:
					break out
				}
			}

			if config.BotConfig.DebugMode {
				contentLock.Lock()
				// content += fmt.Sprintf("\n\nusage: %d + %d = %d\n", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
				content += fmt.Sprintf("\n\nwait: %v\n", time.Since(start))
				util.EditMessageWithError(ctx.msg, content)
				contentLock.Unlock()
			}

		}(ctx)
	}
}
