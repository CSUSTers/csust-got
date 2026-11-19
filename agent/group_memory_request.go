package agentv3

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"csust-got/util"
	tb "gopkg.in/telebot.v3"
)

type groupMemoryRequest struct {
	action, id, category, directory, description, content string
	summarize                                             bool
}

// The outer grammar is anchored: text inside a quote, an example or an earlier
// turn must never select a mutation. The payload after the delimiter is data.
const memoryPathPattern = `(?:\[([^\[\]/>\r\n]+)[>/]([^\[\]/>\r\n]+)\])?`
const memoryIDPattern = `([a-zA-Z0-9_-]+)`

var (
	addMemoryRequest       = regexp.MustCompile(`(?s)^新增群记忆[ \t]*` + memoryPathPattern + `[ \t]*(?:（([^（）\r\n]+)）)?[ \t]*[:：](.+)$`)
	replaceMemoryRequest   = regexp.MustCompile(`(?s)^替换群记忆[ \t]*` + memoryPathPattern + `[ \t]*` + memoryIDPattern + `[ \t]*[:：](.+)$`)
	forgetMemoryRequest    = regexp.MustCompile(`^删除群记忆[ \t]*` + memoryPathPattern + `[ \t]*` + memoryIDPattern + `$`)
	summarizeMemoryRequest = regexp.MustCompile(`^总结本次会话并记入群记忆[ \t]*` + memoryPathPattern + `$`)
	saveReplyRequest       = regexp.MustCompile(`^把这条记到(?:群记忆)?[ \t]*` + memoryPathPattern + `$`)
)

func parseGroupMemoryRequest(tc *TurnContext) (groupMemoryRequest, bool) {
	msg := tc.Message
	if !directGroupMemoryMessage(msg) {
		return groupMemoryRequest{}, false
	}
	raw, entities := msg.Text, msg.Entities
	if raw == "" {
		raw, entities = msg.Caption, msg.CaptionEntities
	}
	header := strings.TrimSpace(raw)
	text := header
	if tc.Trigger != nil && tc.Trigger.Command != "" {
		cmd, payload, found := strings.Cut(text, " ")
		name, bot, _ := strings.Cut(cmd, "@")
		if !found || name != "/"+strings.TrimPrefix(tc.Trigger.Command, "/") || (bot != "" && (tc.BotUser == nil || !strings.EqualFold(tc.BotUser.Username, bot))) {
			return groupMemoryRequest{}, false
		}
		text = strings.TrimSpace(payload)
	}
	polite := strings.HasPrefix(text, "请") || strings.HasPrefix(text, "帮我")
	text = strings.TrimSpace(strings.TrimPrefix(text, "请"))
	text = strings.TrimSpace(strings.TrimPrefix(text, "帮我"))
	var req groupMemoryRequest
	var replyContent string
	switch {
	case strings.HasPrefix(text, "记住"):
		body := strings.TrimSpace(strings.TrimPrefix(text, "记住"))
		switch {
		case strings.HasPrefix(body, "："):
			body = strings.TrimPrefix(body, "：")
		case strings.HasPrefix(body, ":"):
			body = strings.TrimPrefix(body, ":")
		case !polite || strings.HasSuffix(body, "？") || strings.HasSuffix(body, "?") || strings.HasSuffix(body, "吗"):
			return groupMemoryRequest{}, false
		}
		req = groupMemoryRequest{action: groupMemoryActionAdd, content: body}
	case strings.HasPrefix(text, "新增群记忆"):
		if m := addMemoryRequest.FindStringSubmatch(text); m != nil {
			req = groupMemoryRequest{action: groupMemoryActionAdd, category: m[1], directory: m[2], description: m[3], content: m[4]}
		}
	case strings.HasPrefix(text, "替换群记忆"):
		if m := replaceMemoryRequest.FindStringSubmatch(text); m != nil {
			req = groupMemoryRequest{action: groupMemoryActionReplace, category: m[1], directory: m[2], id: m[3], content: m[4]}
		}
	case strings.HasPrefix(text, "删除群记忆"):
		if m := forgetMemoryRequest.FindStringSubmatch(text); m != nil {
			req = groupMemoryRequest{action: groupMemoryActionForget, category: m[1], directory: m[2], id: m[3]}
		}
	case strings.HasPrefix(text, "总结本次会话并记入群记忆"):
		if m := summarizeMemoryRequest.FindStringSubmatch(text); m != nil {
			req = groupMemoryRequest{action: groupMemoryActionAdd, summarize: true, category: m[1], directory: m[2]}
		}
	case strings.HasPrefix(text, "把这条记到"):
		m := saveReplyRequest.FindStringSubmatch(text)
		reply, ok := replySessionEmbeddedParent(msg.ReplyTo, msg.Chat)
		if m != nil && ok && (m[1] != "" || strings.Contains(text, "群记忆")) {
			content := reply.Text
			if content == "" {
				content = reply.Caption
			}
			if msg.Quote != nil {
				if !strings.Contains(content, msg.Quote.Text) {
					return groupMemoryRequest{}, false
				}
				content = msg.Quote.Text
			}
			req = groupMemoryRequest{action: groupMemoryActionAdd, category: m[1], directory: m[2]}
			replyContent = content
		}
	}
	if req.action == "" {
		return groupMemoryRequest{}, false
	}
	pathGiven := req.category != ""
	req.category, req.directory, req.description = strings.TrimSpace(req.category), strings.TrimSpace(req.directory), strings.TrimSpace(req.description)
	if pathGiven && (req.category == "" || req.directory == "") {
		return groupMemoryRequest{}, false
	}
	if len(entities) != 0 {
		leadingBytes := len(raw) - len(strings.TrimLeftFunc(raw, unicode.IsSpace))
		start := util.UTF16Len(raw[:leadingBytes])
		end := start + util.UTF16Len(strings.TrimSuffix(header, req.content))
		blockedTypes := []tb.EntityType{
			tb.EntityBlockquote, "expandable_blockquote", tb.EntityCode, tb.EntityCodeBlock,
			tb.EntityBold, tb.EntityItalic, tb.EntityUnderline, tb.EntityStrikethrough, tb.EntitySpoiler, tb.EntityTextLink,
		}
		for _, entity := range entities {
			if slices.Contains(blockedTypes, entity.Type) && entity.Offset < end && entity.Offset+entity.Length > start {
				return groupMemoryRequest{}, false
			}
		}
	}
	if replyContent != "" {
		req.content = replyContent
	}
	req.content = strings.TrimSpace(req.content)
	return req, req.summarize || req.action == groupMemoryActionForget || req.content != ""
}
