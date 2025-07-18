package chat

import (
	"bytes"
	"context"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/base64"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	"golang.org/x/image/draw"

	_ "golang.org/x/image/webp"
	tb "gopkg.in/telebot.v3"
)

var clients map[string]*openai.Client
var templates *xsync.Map[string, chatTemplate]

type chatTemplate struct {
	PromptTemplate       *template.Template
	SystemPromptTemplate *template.Template
}

func getTemplate(c *config.ChatConfigSingle, cache bool) (chatTemplate, error) {
	tpl, ok := templates.Load(c.Name)
	if ok {
		return tpl, nil
	}
	var ret chatTemplate
	if c.SystemPrompt != "" {
		p, err := template.New("system-prompt").Parse(c.SystemPrompt.String())
		if err != nil {
			return ret, err
		}
		ret.SystemPromptTemplate = p
	}

	p, err := template.New("prompt").Parse(c.PromptTemplate.String())
	if err != nil {
		return ret, err
	}
	ret.PromptTemplate = p

	if cache && (ret.PromptTemplate != nil || ret.SystemPromptTemplate != nil) {
		templates.Store(c.Name, ret)
	}
	return ret, nil
}

// InitAiClients 初始化AI客户端
func InitAiClients(configs []*config.ChatConfigSingle) {
	clients = make(map[string]*openai.Client)
	// templates = make(map[string]*template.Template)
	templates = xsync.NewMap[string, chatTemplate](xsync.WithPresize(len(configs)))

	for _, c := range configs {
		// 初始化模板
		if _, ok := templates.Load(c.Name); !ok {
			var sysPrompt *template.Template
			if c.SystemPrompt != "" {
				sysPrompt = template.Must(template.New("systemPrompt").Parse(c.SystemPrompt.String()))
			}
			templates.Store(c.Name, chatTemplate{
				PromptTemplate:       template.Must(template.New("prompt").Parse(c.PromptTemplate.String())),
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
	ReplyToXml      string
	BotUsername     string // 添加 Bot 用户名字段
}

// Chat 处理聊天请求
func Chat(ctx tb.Context, v2 *config.ChatConfigSingle, trigger *config.ChatTrigger) error {

	// 检查白名单
	if v2.Model.Features.WhiteList {
		if !config.BotConfig.WhiteListConfig.Check(ctx.Chat().ID) &&
			!config.BotConfig.WhiteListConfig.Check(ctx.Sender().ID) {
			return nil
		}
	}

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

	// if gacha, reply and not send placeholder
	isGacha := trigger.Gacha > 0

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
		ReplyToXml:      FormatSingleTbMessage(ctx.Message().ReplyTo, "REPLY_TO"),
		BotUsername:     ctx.Bot().Me.Username, // 添加 Bot 的用户名
	}

	templs, err := getTemplate(v2, false)
	if err != nil {
		log.Error("chat: parse template failed", zap.String("name", v2.Name))
		return err
	}

	var promptBuf bytes.Buffer
	systemPrompt := v2.SystemPrompt.String()

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

	multiPartContent := false
	var contents []openai.ChatMessagePart
	if v2.Model.Features.Image && v2.Features.Image {
		// TODO handle multi photos album
		msg := ctx.Message()
		for msg.ReplyTo != nil {
			if msg.Photo != nil {
				break
			}
			msg = msg.ReplyTo
		}
		imgs := msg.Photo
		if imgs != nil {
			contents = make([]openai.ChatMessagePart, 0, 2)
			w, h := imgs.Width, imgs.Height
			file, _ := ctx.Bot().File(imgs.MediaFile())
			ori, _, err := image.Decode(file)
			if err != nil {
				log.Error("Failed to decode image", zap.Error(err))
				// TODO handle error
				goto final
			}

			w, h = v2.Features.ImageResize(w, h)
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			log.Info("convert image size", zap.Any("from", ori.Bounds().Size()), zap.Any("to", img.Bounds().Size()))
			draw.ApproxBiLinear.Scale(img, img.Rect, ori, ori.Bounds(), draw.Over, nil)

			buf := bytes.NewBuffer(nil)
			err = jpeg.Encode(buf, img, &jpeg.Options{Quality: 90})
			if err != nil {
				log.Error("Failed to encode image to jpeg", zap.Error(err))
				// TODO handle error
				goto final
			}
			log.Info("encoded jpeg image size", zap.Int("size", buf.Len()))
			base64Img := []byte("data:image/jpeg;base64,")
			base64Img = base64.StdEncoding.AppendEncode(base64Img, buf.Bytes())
			log.Info("encoded base64 image data url size", zap.Int("size", len(base64Img)))
			contents = append(contents,
				openai.ChatMessagePart{
					Type: openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{
						URL: string(base64Img),
					},
				},
				openai.ChatMessagePart{
					Type: openai.ChatMessagePartTypeText,
					Text: promptBuf.String(),
				})

			multiPartContent = true
		}
	}

final:
	if !multiPartContent {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: promptBuf.String(),
		})
	} else {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:         openai.ChatMessageRoleUser,
			MultiContent: contents,
		})
	}

	// zap.L().Debug("Chat context messages", zap.Any("messages", messages))

	client := clients[v2.Model.Name]

	// 处理place_holder功能
	var placeholderMsg *tb.Message
	switch {
	case isGacha:
		// 如果是gacha模式，不使用placeholder
	case v2.PlaceHolder != "":
		// 如果有place_holder，先发送placeholder消息
		var placeHolderErr error
		placeholderMsg, placeHolderErr = ctx.Bot().Reply(ctx.Message(), v2.PlaceHolder, tb.ModeMarkdownV2)
		if placeHolderErr != nil {
			log.Error("Failed to send placeholder message", zap.Error(placeHolderErr))
			// 如果发送placeholder失败，继续正常流程，不使用placeholder功能
		}
	default:
		err = ctx.Bot().Notify(ctx.Chat(), tb.Typing)
		if err != nil {
			log.Error("Failed to send typing notification", zap.Error(err))
		}
	}

	chatCtx, cancel := context.WithTimeout(context.Background(), v2.GetTimeout())
	defer cancel()

	useMcp := v2.UseMcpo && config.BotConfig.McpoServer.Enable

	request := openai.ChatCompletionRequest{
		Model:       v2.Model.Model,
		Messages:    messages,
		Temperature: v2.GetTemperature(),
	}
	if useMcp {
		request.Tools = mcpo.GetToolSet("")
	}
	resp, err := client.CreateChatCompletion(chatCtx, request)
	if err != nil {
		log.Error("Failed to send chat completion message", zap.Error(err))
	}

	// 如果使用了placeholder且出现错误，更新placeholder消息为错误提示
	if placeholderMsg != nil && err != nil {
		_, editErr := util.EditMessageWithError(placeholderMsg, v2.GetErrorMessage(), tb.ModeMarkdownV2)
		if editErr != nil {
			log.Error("Failed to edit placeholder message with error", zap.Error(editErr))
		}
		return err
	} else if err != nil {
		return err
	}

	if len(resp.Choices) == 0 {
		log.Error("No choices in chat completion response")
		if placeholderMsg != nil {
			_, editErr := util.EditMessageWithError(placeholderMsg, v2.GetErrorMessage(), tb.ModeMarkdownV2)
			if editErr != nil {
				log.Error("Failed to edit placeholder message with error", zap.Error(editErr))
			}
		}
		return nil
	}

	// 处理大模型返回工具调用的情况
	for resp.Choices[0].FinishReason == openai.FinishReasonToolCalls && useMcp {
		messages = append(messages, resp.Choices[0].Message)
		for _, toolCall := range resp.Choices[0].Message.ToolCalls {
			var result string
			var err error
			tool, ok := mcpo.GetTool(toolCall.Function.Name)
			if !ok {
				log.Error("MCP tool not found", zap.String("toolName", toolCall.Function.Name))
				result = "MCP tool not found"
				goto finish_each_toolcall
			}
			result, err = tool.Call(chatCtx, toolCall.Function.Arguments)
			if err != nil {
				log.Error("Failed to call tool", zap.String("toolName", toolCall.Function.Name), zap.Error(err))
				result = "Failed to call function tool"
			}
			log.Debug("Tool call result", zap.String("toolName", toolCall.Function.Name), zap.String("result", result))

		finish_each_toolcall:
			toolMsg := openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: toolCall.ID,
				Name:       toolCall.Function.Name,
				Content:    result,
			}
			messages = append(messages, toolMsg)
		}

		request.Messages = messages
		resp, err = client.CreateChatCompletion(chatCtx, request)
		if err != nil {
			log.Error("Failed to send chat completion message", zap.Error(err))
			return err
		}
		if len(resp.Choices) == 0 {
			log.Error("No choices in chat completion response")
			return nil
		}
	}

	response := resp.Choices[0].Message.Content
	// 移除可能的空行
	response = strings.TrimSpace(response)
	response = formatOutput(response, &v2.Format)
	formatOpt := tb.ModeMarkdownV2
	if v2.Format.Format == "html" {
		formatOpt = tb.ModeHTML
	}
	log.Debug("Chat response", zap.String("response", response))

	// 根据是否有placeholder选择更新或发送新消息
	var replyMsg *tb.Message
	if placeholderMsg != nil {
		// 如果使用了placeholder，更新消息
		if response == "" {
			response = v2.GetErrorMessage() // 如果响应为空，使用错误提示消息
		}
		replyMsg, err = util.EditMessageWithError(placeholderMsg, response, formatOpt)
		if err != nil {
			log.Error("Failed to edit placeholder message", zap.Error(err))
			return err
		}
	} else {
		// 如果没有使用placeholder，直接发送响应
		replyMsg, err = ctx.Bot().Reply(ctx.Message(), response, formatOpt)
		if err != nil {
			log.Error("Failed to send reply", zap.Error(err))
			return err
		}
	}

	err = orm.PushMessageToStream(replyMsg)
	if err != nil {
		log.Warn("Store bot's reply message to Redis failed", zap.Error(err))
	}
	return nil

}

var extractReasonPatt = regexp.MustCompile(`(?smi)^\s*<think>\s*(?P<reason>.*?)\s*</think>\s*`)
var reasonGroup = extractReasonPatt.SubexpIndex("reason")

func formatOutput(text string, format *config.ChatOutputFormatConfig) string {
	matches := extractReasonPatt.FindStringSubmatchIndex(text)

	var reason, payload string
	if len(matches) != 0 {
		payload = text[matches[1]:]
		rIdx1, rIdx2 := matches[reasonGroup*2], matches[reasonGroup*2+1]
		reason = text[rIdx1:rIdx2]
	} else {
		payload = text
	}

	buf := strings.Builder{}

	outputFormat := format.GetFormat()
	if outputFormat == "" {
		log.Warn("chat text output format must in [markdown, html], will set to markdown")
		outputFormat = "markdown"
	}

	if reason != "" {
		reasonFormat := format.GetReasonFormat()
		if reasonFormat == "" {
			log.Warn("chat reason output format must in [none, quote, collapse], will set to none")
			reasonFormat = "none"
		}
		switch reasonFormat {
		case "quote":
			formatText(&buf, reason, outputFormat, wholeTextTypeQuote)
		case "collapse":
			formatText(&buf, reason, outputFormat, wholeTextTypeCollapse)
		default:
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
	}

	payloadFormat := format.GetPayloadFormat()
	if payloadFormat == "" {
		log.Warn("chat payload output format must in [plain, quote, collapse, block], will set to plain")
		payloadFormat = "plain"
	}

	payloadType := wholeTextTypePlain
	switch payloadFormat {
	case "quote":
		payloadType = wholeTextTypeQuote
	case "collapse":
		payloadType = wholeTextTypeCollapse
	case "block":
		payloadType = wholeTextTypeBlock
	case "markdown-block":
		payloadType = wholeTextTypeMdBlock
	}

	formatText(&buf, payload, outputFormat, payloadType)
	return buf.String()
}

type wholeTextType string

const (
	wholeTextTypePlain    wholeTextType = "plain"
	wholeTextTypeQuote    wholeTextType = "quote"
	wholeTextTypeCollapse wholeTextType = "collapse"
	wholeTextTypeBlock    wholeTextType = "block"
	wholeTextTypeMdBlock  wholeTextType = "markdown-block"
)

func formatText(buf *strings.Builder, text string, format string, t wholeTextType) {
	if len(text) == 0 {
		return
	}
	switch format {
	case "markdown":
		switch t {
		case wholeTextTypePlain:
			buf.WriteString(util.EscapeTgMDv2ReservedChars(text))
		case wholeTextTypeCollapse:
			buf.WriteString("**")
			fallthrough
		case wholeTextTypeQuote:
			lines := strings.Lines(text)
			for line := range lines {
				buf.WriteString(">")
				buf.WriteString(util.EscapeTgMDv2ReservedChars(line))
			}
			if t == wholeTextTypeCollapse {
				if text[len(text)-1] == '\n' {
					buf.WriteString(">")
				}
				buf.WriteString("||")
			}
			buf.WriteString("\n")
		case wholeTextTypeBlock, wholeTextTypeMdBlock:
			buf.WriteString("```")
			if t == wholeTextTypeMdBlock {
				buf.WriteString("markdown")
			}
			buf.WriteString("\n")
			buf.WriteString(util.EscapeTgMDv2ReservedChars(text))
			buf.WriteString("\n```\n")
		}
	case "html":
		switch t {
		case wholeTextTypePlain:
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
		case wholeTextTypeCollapse:
			buf.WriteString("<blockquote expandable>")
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
			buf.WriteString("</blockquote>")
		case wholeTextTypeQuote:
			buf.WriteString("<blockquote>")
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
			buf.WriteString("</blockquote>")
		case wholeTextTypeBlock, wholeTextTypeMdBlock:
			buf.WriteString("<pre>")
			if t == wholeTextTypeMdBlock {
				buf.WriteString(`<code class="language-markdown">`)
			}
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
			if t == wholeTextTypeMdBlock {
				buf.WriteString(`</code class="language-markdown">`)
			}
			buf.WriteString("</pre>")
		}
	default:
		buf.WriteString(text)
	}
}
