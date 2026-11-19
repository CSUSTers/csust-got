package agentv3

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"csust-got/config"
	"csust-got/orm"
	tb "gopkg.in/telebot.v3"
)

const (
	groupMemoryActionAdd     = "add"
	groupMemoryActionReplace = "replace"
	groupMemoryActionForget  = "forget"
)

var (
	errGroupMemoryDisabled         = errors.New("群记忆已关闭。")
	errGroupMemoryUnknownSender    = errors.New("无法确认当前聊天或发送者。")
	errGroupMemoryInvalidDirectory = errors.New("请指定分类和目录，并使用单行目录说明。")
	errGroupMemoryEmptyContent     = errors.New("条目内容不能为空。")
	errGroupMemoryInvalidAdd       = errors.New("新增时请提供目录说明，条目 ID 将自动生成。")
	errGroupMemoryMissingID        = errors.New("请指定条目完整 ID。")
	errGroupMemoryUnknownAction    = errors.New("未知的群记忆操作。")
)

type groupMemoryDenialError string

func (denial groupMemoryDenialError) Error() string { return string(denial) }

func manageGroupMemory(ctx context.Context, scope orm.AgentV3Scope, chat *tb.Chat, sender *tb.User, action, category, directory, description, id, content string) (string, error) {
	if !groupMemoryEnabled() {
		return "", errGroupMemoryDisabled
	}
	if chat == nil || chat.ID != scope.ChatID || sender == nil || sender.ID == 0 {
		return "", errGroupMemoryUnknownSender
	}
	admin := agentV3IsChatAdmin(chat, sender)
	if denial := agentV3MemoryPolicyDenial(admin); denial != "" {
		return "", groupMemoryDenialError(denial)
	}
	category, directory, description = strings.TrimSpace(category), strings.TrimSpace(directory), strings.TrimSpace(description)
	if category == "" || directory == "" || strings.ContainsAny(category+directory, ">/[]\r\n") || strings.ContainsAny(description, "\r\n") {
		return "", errGroupMemoryInvalidDirectory
	}
	content = strings.TrimSpace(content)
	if (action == groupMemoryActionAdd || action == groupMemoryActionReplace) && content == "" {
		return "", errGroupMemoryEmptyContent
	}
	var denial string
	var err error
	switch action {
	case groupMemoryActionAdd:
		if id != "" || description == "" {
			return "", errGroupMemoryInvalidAdd
		}
		id = newAgentV3MemoryID()
		item := orm.AgentV3MemoryItem{ID: id, Category: category, Directory: directory, Description: description, Content: content, CreatedBy: sender.ID, CreatedAt: time.Now()}
		denial, err = orm.AddGroupMemory(ctx, scope, item, func(items []orm.AgentV3MemoryItem) string {
			return agentV3MemoryWriteDenial(items, admin, sender.ID, content)
		})
	case groupMemoryActionForget, groupMemoryActionReplace:
		if id == "" {
			return "", errGroupMemoryMissingID
		}
		var replacement *string
		if action == groupMemoryActionReplace {
			replacement = &content
		}
		denial, err = orm.ChangeGroupMemory(ctx, scope, category, directory, id, replacement, func(items []orm.AgentV3MemoryItem) string {
			for i, item := range items {
				if item.ID != id {
					continue
				}
				if !admin && sender.ID != item.CreatedBy {
					return "只能修改或删除自己添加的群记忆。"
				}
				if replacement != nil {
					// Replacing a fact consumes no new slot, only its updated content size.
					others := slices.Concat(items[:i], items[i+1:])
					return agentV3MemoryWriteDenial(others, true, sender.ID, content)
				}
				return ""
			}
			return "该目录中没有这个条目 ID。"
		})
	default:
		return "", errGroupMemoryUnknownAction
	}
	if err != nil {
		return "", err
	}
	if denial != "" {
		return "", groupMemoryDenialError(denial)
	}
	return id, nil
}

func groupMemoryEnabled() bool {
	return config.BotConfig != nil && config.BotConfig.AgentV3 != nil && config.BotConfig.AgentV3.Memory.Enable
}

func directGroupMemoryMessage(msg *tb.Message) bool {
	return msg != nil && msg.Chat != nil && msg.Sender != nil && !msg.Sender.IsBot && msg.SenderChat == nil && msg.Via == nil && !msg.IsForwarded() && msg.Origin == nil && !msg.AutomaticForward
}
