package chat

import (
	"csust-got/config"
	"csust-got/log"
	"regexp"
	"strings"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

var (
	// 匹配"...是什么"和"什么是..."两种格式的问题
	whatIsPattern    = regexp.MustCompile(`(.+)是什么[\?？]*$`)
	whatIsPatternAlt = regexp.MustCompile(`^什么是(.+)[\?？]*$`)
)

// WhatIsHandler 处理"...是什么"或"什么是..."格式的问题
func WhatIsHandler(ctx Context) error {
	msg := ctx.Message()

	// 仅处理文本消息
	var text string
	if len(msg.Text) > 0 {
		text = msg.Text
	} else if len(msg.Caption) > 0 {
		text = msg.Caption
	}

	if len(text) == 0 || strings.HasPrefix(text, "/") {
		return nil
	}

	// 提取需要解释的概念
	var concept string

	// 首先匹配"...是什么"格式
	matches := whatIsPattern.FindStringSubmatch(text)
	if len(matches) >= 2 {
		concept = matches[1]
	} else {
		// 如果不匹配，尝试匹配"什么是..."格式
		matches = whatIsPatternAlt.FindStringSubmatch(text)
		if len(matches) < 2 {
			// 都不匹配，不处理
			return nil
		}
		concept = matches[1]
	}

	// 概念太长，可能不是正常问题，忽略
	if len(concept) > 30 {
		return nil
	}

	// 构建基础提问文本
	prompt := "请简明扼要地解释一下「" + concept + "」是什么"

	// 获取消息上下文
	contextMessages, err := GetMessageContext(config.BotConfig.Bot, msg)
	if err != nil {
		log.Error("[WhatIs] Failed to get message context", zap.Error(err))
		// 继续执行，只是没有上下文而已
	}

	// 如果有上下文，则添加到提问中
	if len(contextMessages) > 1 { // 至少有当前消息和一条上下文消息
		contextText := FormatContextMessages(contextMessages[:len(contextMessages)-1]) // 不包括当前消息
		prompt = "在以下对话的上下文中，请简明扼要地解释「" + concept + "」是什么，\n\n对话记录：\n\n" +
			contextText + "\n\n用户问题：" + msg.Text
	}

	systemPrompt := `你需要帮用户解释指定的概念是什么。请遵循以下规则：
	1、回答应当尽可能简短，直接陈述结论，不要进行多余的解释。
	2、不要使用markdown格式进行回答。
	3、尽可能避免在回答中使用特殊字符。
	4、上下文中可能包含一些不相关的信息，请自行判断后忽略。
	5、在回答中不要引用消息编号。`

	// 调用ChatGPT解答
	err = ChatWith(ctx, &ChatInfo{
		Text: prompt,
		Setting: Setting{
			Stream:       false,
			Reply:        true,
			SystemPrompt: systemPrompt,
			Temperature:  0.5,
		},
	})

	if err != nil {
		return err
	}

	return nil
}
