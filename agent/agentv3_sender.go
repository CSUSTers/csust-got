package agentv3

import (
	"fmt"
	"html"
	"strings"

	"csust-got/orm"

	tb "gopkg.in/telebot.v3"
)

type agentV3Sender struct {
	ID       int64
	Username string
	ShowName string
}

var agentV3SenderLabelReplacer = strings.NewReplacer("[", "(", "]", ")", "\r", " ", "\n", " ", "\t", " ")

func agentV3MessageSender(msg *tb.Message) (agentV3Sender, bool) {
	if msg == nil {
		return agentV3Sender{}, false
	}
	if chat := msg.SenderChat; chat != nil {
		return agentV3Sender{ID: chat.ID, Username: strings.TrimSpace(chat.Username), ShowName: strings.TrimSpace(chat.Title)}, true
	}
	if user := msg.Sender; user != nil {
		return agentV3Sender{
			ID:       user.ID,
			Username: strings.TrimSpace(user.Username),
			ShowName: strings.TrimSpace((&userNames{First: user.FirstName, Last: user.LastName}).ShowName()),
		}, true
	}
	return agentV3Sender{}, false
}

func (s agentV3Sender) label() string {
	parts := make([]string, 0, 2)
	if s.Username != "" {
		parts = append(parts, "@"+s.Username)
	}
	if s.ID != 0 {
		parts = append(parts, fmt.Sprintf("id %d", s.ID))
	}
	label := s.ShowName
	if len(parts) > 0 {
		label = strings.TrimSpace(fmt.Sprintf("%s (%s)", label, strings.Join(parts, ", ")))
	}
	return strings.TrimSpace(agentV3SenderLabelReplacer.Replace(label))
}

func agentV3CurrentSenderBlock(tc *TurnContext) string {
	if tc == nil || tc.Background {
		return ""
	}
	sender, ok := agentV3MessageSender(tc.Message)
	if !ok {
		return ""
	}
	return fmt.Sprintf(`<current_sender id="%d" username="%s" showname="%s" />`, sender.ID,
		html.EscapeString(sender.Username), html.EscapeString(sender.ShowName))
}

func agentV3TurnSender(tc *TurnContext) string {
	if tc == nil || tc.Background {
		return ""
	}
	sender, ok := agentV3MessageSender(tc.Message)
	if !ok {
		return ""
	}
	return sender.label()
}

func agentV3TurnSummaryRole(turn orm.AgentV3Turn) string {
	role := turn.Role
	if role == "" {
		role = "user"
	}
	if sender := strings.TrimSpace(turn.Sender); sender != "" && role != "assistant" {
		return fmt.Sprintf("%s [%s]", role, sender)
	}
	return role
}
