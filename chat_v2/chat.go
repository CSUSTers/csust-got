package chat_v2

import (
	"bytes"
	"context"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	"golang.org/x/image/draw"

	_ "golang.org/x/image/webp"
	tb "gopkg.in/telebot.v3"
)

var clients map[string]*openai.Client
var templates *xsync.Map[string, chatTemplate]

// copy from [Cherry Studio](https://github.com/CherryHQ/cherry-studio/blob/0744e42be94cff22d1a94b6fefa422647e138f03/src/renderer/src/utils/prompt.ts#L3)
const (
	// nolint:gosec
	toolUseExample = `
## Tool Use Examples

Here are a few examples using notional tools:
---
User: Generate an image of the oldest person in this document.

Assistant: I can use the document_qa tool to find out who the oldest person is in the document.
<tool_use>
  <name>document_qa</name>
  <arguments>{"document": "document.pdf", "question": "Who is the oldest person mentioned?"}</arguments>
</tool_use>

User: <tool_use_result>
  <name>document_qa</name>
  <result>John Doe, a 55 year old lumberjack living in Newfoundland.</result>
</tool_use_result>

Assistant: I can use the image_generator tool to create a portrait of John Doe.
<tool_use>
  <name>image_generator</name>
  <arguments>{"prompt": "A portrait of John Doe, a 55-year-old man living in Canada."}</arguments>
</tool_use>

User: <tool_use_result>
  <name>image_generator</name>
  <result>image.png</result>
</tool_use_result>

Assistant: the image is generated as image.png

---
User: "What is the result of the following operation: 5 + 3 + 1294.678?"

Assistant: I can use the python_interpreter tool to calculate the result of the operation.
<tool_use>
  <name>python_interpreter</name>
  <arguments>{"code": "5 + 3 + 1294.678"}</arguments>
</tool_use>

User: <tool_use_result>
  <name>python_interpreter</name>
  <result>1302.678</result>
</tool_use_result>

Assistant: The result of the operation is 1302.678.

---
User: "Which city has the highest population , Guangzhou or Shanghai?"

Assistant: I can use the search tool to find the population of Guangzhou.
<tool_use>
  <name>search</name>
  <arguments>{"query": "Population Guangzhou"}</arguments>
</tool_use>

User: <tool_use_result>
  <name>search</name>
  <result>Guangzhou has a population of 15 million inhabitants as of 2021.</result>
</tool_use_result>

Assistant: I can use the search tool to find the population of Shanghai.
<tool_use>
  <name>search</name>
  <arguments>{"query": "Population Shanghai"}</arguments>
</tool_use>

User: <tool_use_result>
  <name>search</name>
  <result>26 million (2019)</result>
</tool_use_result>
Assistant: The population of Shanghai is 26 million, while Guangzhou has a population of 15 million. Therefore, Shanghai has the highest population.`

	toolUsePromptTemplate = `
In this environment you have access to a set of tools you can use to answer the user's question. You can use one tool per message, and will receive the result of that tool use in the user's response. You use tools step-by-step to accomplish a given task, with each tool use informed by the result of the previous tool use.

## Tool Use Formatting

Tool use is formatted using XML-style tags. The tool name is enclosed in opening and closing tags, and each parameter is similarly enclosed within its own set of tags. Here's the structure:

<tool_use>
  <name>{tool_name}</name>
  <arguments>{json_arguments}</arguments>
</tool_use>

The tool name should be the exact name of the tool you are using, and the arguments should be a JSON object containing the parameters required by that tool. For example:
<tool_use>
  <name>python_interpreter</name>
  <arguments>{"code": "5 + 3 + 1294.678"}</arguments>
</tool_use>

The user will respond with the result of the tool use, which should be formatted as follows:

<tool_use_result>
  <name>{tool_name}</name>
  <result>{result}</result>
</tool_use_result>

The result should be a string, which can represent a file or any other output type. You can use this result as input for the next action.
For example, if the result of the tool use is an image file, you can use it in the next action like this:

<tool_use>
  <name>image_transformer</name>
  <arguments>{"image": "image_1.jpg"}</arguments>
</tool_use>

Always adhere to this format for the tool use to ensure proper parsing and execution.` + toolUseExample + `
## Tool Use Available Tools
Above example were using notional tools that might not exist for you. You only have access to these tools:
{{AVAILABLE_TOOLS}}

## Tool Use Rules
Here are the rules you should always follow to solve your task:
1. Always use the right arguments for the tools. Never use variable names as the action arguments, use the value instead.
2. Call a tool only when needed: do not call the search agent if you do not need information, try to solve the task yourself.
3. If no tool call is needed, just answer the question directly.
4. Never re-do a tool call that you previously did with the exact same parameters.
5. For tool use, MARK SURE use XML tag format as shown in the examples above. Do not use any other format.

# User Instructions
{{USER_SYSTEM_PROMPT}}

Now Begin! If you solve the task correctly, you will receive a reward of $1,000,000.`
)

func getToolUseSystemPrompt(userSystemPrompt string) string {
	if mcpToolsDesc == "" {
		return userSystemPrompt
	}

	replacer := strings.NewReplacer("{{AVAILABLE_TOOLS}}", mcpToolsDesc, "{{USER_SYSTEM_PROMPT}}", userSystemPrompt)
	return replacer.Replace(toolUsePromptTemplate)
}

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
		p, err := template.New("system-prompt").Parse(c.SystemPrompt)
		if err != nil {
			return ret, err
		}
		ret.SystemPromptTemplate = p
	}

	p, err := template.New("prompt").Parse(c.PromptTemplate)
	if err != nil {
		return ret, err
	}
	ret.PromptTemplate = p

	if cache && (ret.PromptTemplate != nil || ret.SystemPromptTemplate != nil) {
		templates.Store(c.Name, ret)
	}
	return ret, nil
}

// var templates map[string]*template.Template

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

	useMcp := v2.Model.Features.Mcp
	if useMcp && v2.Features.Mcp != nil {
		useMcp = *v2.Features.Mcp
	}
	usePromptTool := v2.Features.UsePromptTool

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
		ReplyToXml:      FormatSingleTbMessage(ctx.Message().ReplyTo, "REPLY_TO"),
		BotUsername:     ctx.Bot().Me.Username, // 添加 Bot 的用户名
	}

	templs, err := getTemplate(v2, false)
	if err != nil {
		log.Error("chat: parse template failed", zap.String("name", v2.Name))
		return err
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

	if usePromptTool {
		systemPrompt = getToolUseSystemPrompt(systemPrompt)
		log.Debug("mcp systen promplt", zap.String("system prompt", systemPrompt))
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
	if v2.PlaceHolder != "" {
		// 如果有place_holder，先发送placeholder消息
		var placeHolderErr error
		placeholderMsg, placeHolderErr = ctx.Bot().Reply(ctx.Message(), v2.PlaceHolder, tb.ModeMarkdownV2)
		if placeHolderErr != nil {
			log.Error("Failed to send placeholder message", zap.Error(placeHolderErr))
			// 如果发送placeholder失败，继续正常流程，不使用placeholder功能
		}
	} else {
		err = ctx.Bot().Notify(ctx.Chat(), tb.Typing)
		if err != nil {
			log.Error("Failed to send typing notification", zap.Error(err))
		}
	}

	chatCtx, cancel := context.WithTimeout(context.Background(), v2.GetTimeout())
	defer cancel()

	request := openai.ChatCompletionRequest{
		Model:       v2.Model.Model,
		Messages:    messages,
		Temperature: v2.GetTemperature(),
	}
	if useMcp {
		request.Tools = allTools
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

	// nolint:nestif
	if usePromptTool {
		type ToolCall struct {
			Name      string `xml:"name"`
			Arguments string `xml:"arguments"`
		}
		for {
			ch := resp.Choices[0]
			switch ch.FinishReason {
			case openai.FinishReasonLength, openai.FinishReasonNull, openai.FinishReasonStop:
				log.Info("use tool prompt break", zap.String("reason", string(ch.FinishReason)))
				goto chat_finish
			default:
			}
			log.Debug("prompt tool resp", zap.Any("msg", &ch.Message))
			msgContent := ch.Message.Content
			toolCall := ToolCall{}
			err := xml.Unmarshal([]byte(msgContent), &toolCall)
			if err != nil || toolCall.Name == "" {
				log.Info("cannot parse tool call request, maybe normal msg",
					zap.String("content", msgContent), zap.Error(err))
				goto chat_finish
			}

			clientName, ok := toolsClientMap[toolCall.Name]
			c, ok2 := mcpClients[clientName]
			if !ok || !ok2 {
				log.Error("cannot find tool client request by llm", zap.String("tool", toolCall.Name))
				goto chat_finish
			}

			toolReq := mcp.CallToolRequest{}
			toolReq.Params.Name = toolCall.Name
			var callResult *mcp.CallToolResult
			var callResultResp string
			if toolCall.Arguments != "" {
				args := make(map[string]any)
				err := json.Unmarshal([]byte(toolCall.Arguments), &args)
				if err != nil {
					log.Error("Failed to unmarshal tool arguments", zap.String("arguments", toolCall.Arguments), zap.Error(err))
					goto tool_call_end
				}
				toolReq.Params.Arguments = args
			}
			callResult, err = c.CallTool(chatCtx, toolReq)
			if err != nil {
				log.Error("Failed to call tool", zap.String("toolName", toolCall.Name), zap.Error(err))
				continue
			}
			// 处理工具调用结果
			if callResult.IsError {
				log.Error("Tool call error", zap.String("toolName", toolCall.Name), zap.Any("result", callResult.Result))
				goto tool_call_end
			}
			log.Debug("Tool call result", zap.String("toolName", toolCall.Name), zap.Any("result", callResult))
			{
				content, err := json.Marshal(callResult.Content)
				if err != nil {
					log.Error("Failed to marshal tool call result", zap.String("toolName", toolCall.Name), zap.Error(err))
					goto tool_call_end
				}
				callResultResp = ("<tool_use_result>\n" +
					"  <name>" + toolCall.Name + "</name>\n" +
					"  <result>" + string(content) + "</result>\n" +
					"</tool_use_result>")
			}

		tool_call_end:
			if callResultResp == "" {
				log.Error("failed to call mcp tool")
				goto chat_finish
			}
			request.Messages = append(request.Messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: callResultResp,
			})
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
	} else if useMcp {
		// 处理大模型返回工具调用的情况
		for resp.Choices[0].FinishReason == openai.FinishReasonToolCalls {
			messages = append(messages, resp.Choices[0].Message)
			for _, toolCall := range resp.Choices[0].Message.ToolCalls {
				c, ok := mcpClients[toolsClientMap[toolCall.Function.Name]]
				if !ok {
					log.Error("MCP client not found", zap.String("toolName", toolCall.Function.Name))
					continue
				}
				// 调用工具 {"timezone": "Asia/Shanghai"}
				toolReq := mcp.CallToolRequest{}
				toolReq.Params.Name = toolCall.Function.Name
				if toolCall.Function.Arguments != "" {
					args := make(map[string]any)
					err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args)
					if err != nil {
						log.Error("Failed to unmarshal tool arguments", zap.String("arguments", toolCall.Function.Arguments), zap.Error(err))
						continue
					}
					toolReq.Params.Arguments = args
				}
				result, err := c.CallTool(chatCtx, toolReq)
				if err != nil {
					log.Error("Failed to call tool", zap.String("toolName", toolCall.Function.Name), zap.Error(err))
					continue
				}
				// 处理工具调用结果
				if result.IsError {
					log.Error("Tool call error", zap.String("toolName", toolCall.Function.Name), zap.Any("result", result.Result))
					continue
				}
				log.Debug("Tool call result", zap.String("toolName", toolCall.Function.Name), zap.Any("result", result))
				content, err := json.Marshal(result.Content)
				if err != nil {
					log.Error("Failed to marshal tool call result", zap.String("toolName", toolCall.Function.Name), zap.Error(err))
					continue
				}
				toolMsg := openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: toolCall.ID,
					Name:       toolCall.Function.Name,
					Content:    string(content),
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
	}

chat_finish:

	response := resp.Choices[0].Message.Content
	// 移除可能的空行
	response = strings.TrimSpace(response)
	response = util.EscapeTelegramReservedChars(response)
	log.Debug("Chat response", zap.String("response", response))

	// 根据是否有placeholder选择更新或发送新消息
	var replyMsg *tb.Message
	if placeholderMsg != nil {
		// 如果使用了placeholder，更新消息
		if response == "" {
			response = v2.GetErrorMessage() // 如果响应为空，使用错误提示消息
		}
		replyMsg, err = util.EditMessageWithError(placeholderMsg, response, tb.ModeMarkdownV2)
		if err != nil {
			log.Error("Failed to edit placeholder message", zap.Error(err))
			return err
		}
	} else {
		// 如果没有使用placeholder，直接发送响应
		replyMsg, err = ctx.Bot().Reply(ctx.Message(), response, tb.ModeMarkdownV2)
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
