package agentv3

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	tb "gopkg.in/telebot.v3"
)

func collectAgentV3ImageRefs(tc *TurnContext, history *RichHistory, rawTurns []orm.AgentV3Turn) []orm.AgentV3ImageRef {
	if tc == nil || tc.Message == nil {
		return nil
	}
	if history == nil {
		history = &RichHistory{}
	}

	seenMessageIDs := make(map[int]struct{})
	seenFileIDs := make(map[string]struct{})
	refs := make([]orm.AgentV3ImageRef, 0)
	collect := func(messages []*tb.Message, skipFileIDs map[string]struct{}) {
		for _, message := range messages {
			if message == nil || message.Photo == nil {
				continue
			}
			fileID := strings.TrimSpace(message.Photo.FileID)
			if fileID == "" {
				continue
			}
			if _, skip := skipFileIDs[fileID]; skip {
				continue
			}
			if _, seen := seenMessageIDs[message.ID]; seen {
				continue
			}
			if _, seen := seenFileIDs[fileID]; seen {
				continue
			}
			seenMessageIDs[message.ID] = struct{}{}
			seenFileIDs[fileID] = struct{}{}
			refs = append(refs, orm.AgentV3ImageRef{MessageID: message.ID, FileID: fileID})
		}
	}

	collect(loadCurrentAlbumMessages(tc.Message), nil)
	if tc.Message.ReplyTo != nil {
		collect([]*tb.Message{tc.Message.ReplyTo}, nil)
	}
	collect(history.FullMessages, agentV3RawTurnFileIDs(rawTurns))
	return refs
}

func agentV3RawTurnFileIDs(turns []orm.AgentV3Turn) map[string]struct{} {
	fileIDs := make(map[string]struct{})
	for _, turn := range turns {
		for _, ref := range normalizeAgentV3ImageRefs(turn.ImageRefs) {
			fileIDs[ref.FileID] = struct{}{}
		}
	}
	return fileIDs
}

func appendAgentV3ImageRefsToUserMessage(msg *schema.Message, dynamicText string, tc *TurnContext, refs []orm.AgentV3ImageRef) *schema.Message {
	imageRefs := agentV3ImageRefsContext(refs)
	if len(msg.UserInputMultiContent) == 0 {
		return schema.UserMessage(joinUserMessageSections(dynamicText, imageRefs, strings.Join(collectDocumentHints(tc), "\n")))
	}
	if imageRefs != "" {
		msg.UserInputMultiContent[0].Text = joinUserMessageSections(msg.UserInputMultiContent[0].Text, imageRefs)
	}
	return msg
}

func agentV3UserTurnPromptContent(turn orm.AgentV3Turn) string {
	return joinUserMessageSections(strings.TrimSpace(turn.Content), agentV3ImageRefsContext(turn.ImageRefs))
}

func agentV3TurnPromptContent(turn orm.AgentV3Turn) string {
	if turn.Role == string(schema.Assistant) {
		return strings.TrimSpace(turn.Content)
	}
	return agentV3UserTurnPromptContent(turn)
}

func agentV3TurnImageRefs(tc *TurnContext) []orm.AgentV3ImageRef {
	if tc == nil || tc.V3 == nil || len(tc.V3.ImageRefs) == 0 {
		return nil
	}
	return normalizeAgentV3ImageRefs(tc.V3.ImageRefs)
}

func normalizeAgentV3ImageRefs(refs []orm.AgentV3ImageRef) []orm.AgentV3ImageRef {
	seenFileIDs := make(map[string]struct{})
	result := make([]orm.AgentV3ImageRef, 0, len(refs))
	for _, ref := range refs {
		ref.FileID = strings.TrimSpace(ref.FileID)
		if ref.FileID == "" {
			continue
		}
		if _, seen := seenFileIDs[ref.FileID]; seen {
			continue
		}
		seenFileIDs[ref.FileID] = struct{}{}
		result = append(result, ref)
	}
	return result
}

func agentV3ImageRefsContext(refs []orm.AgentV3ImageRef) string {
	refs = normalizeAgentV3ImageRefs(refs)
	if len(refs) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<telegram_image_refs>\n")
	builder.WriteString("Use get_image or analyze_image with a file_id below when the image is needed.\n")
	for _, ref := range refs {
		fmt.Fprintf(&builder, "- message_id: %d, file_id: %s\n", ref.MessageID, ref.FileID)
	}
	builder.WriteString("</telegram_image_refs>")
	return builder.String()
}

func agentV3TurnsHaveImageRefs(turns []orm.AgentV3Turn) bool {
	for _, turn := range turns {
		if len(normalizeAgentV3ImageRefs(turn.ImageRefs)) > 0 {
			return true
		}
	}
	return false
}

func summarizeAgentV3TurnsWithImageRefs(turns []orm.AgentV3Turn, maxTokens int) string {
	maxChars := approxAgentV3TokenCharLimit(maxTokens)
	lines := make([]string, 0, len(turns))
	usedChars := 0
	seenFileIDs := make(map[string]struct{})
	for _, turn := range turns {
		content := compactAgentV3Text(turn.Content)
		role := turn.Role
		if role == "" {
			role = "user"
		}
		for _, ref := range normalizeAgentV3ImageRefs(turn.ImageRefs) {
			if _, seen := seenFileIDs[ref.FileID]; seen {
				continue
			}
			line := fmt.Sprintf("- %s image: message_id=%d, file_id=%s", role, ref.MessageID, ref.FileID)
			if appendAgentV3SummaryLine(&lines, &usedChars, maxChars, line) {
				seenFileIDs[ref.FileID] = struct{}{}
			}
		}
		if content == "" {
			continue
		}
		appendAgentV3SummaryContent(&lines, &usedChars, maxChars, role, content)
	}
	return strings.Join(lines, "\n")
}

func appendAgentV3SummaryLine(lines *[]string, usedChars *int, maxChars int, line string) bool {
	separatorChars := 0
	if len(*lines) > 0 {
		separatorChars = 1
	}
	if maxChars > 0 && *usedChars+separatorChars+len(line) > maxChars {
		return false
	}
	*lines = append(*lines, line)
	*usedChars += separatorChars + len(line)
	return true
}

func appendAgentV3SummaryContent(lines *[]string, usedChars *int, maxChars int, role, content string) {
	content = truncateAgentV3Text(content, 600)
	prefix := fmt.Sprintf("- %s: ", role)
	if maxChars > 0 {
		separatorChars := 0
		if len(*lines) > 0 {
			separatorChars = 1
		}
		content = truncateAgentV3SummaryContent(content, maxChars-*usedChars-separatorChars-len(prefix))
	}
	if content == "" {
		return
	}
	appendAgentV3SummaryLine(lines, usedChars, maxChars, prefix+content)
}

func truncateAgentV3SummaryContent(content string, maxChars int) string {
	if maxChars <= 0 || len(content) <= maxChars {
		return content
	}
	const marker = "\n[truncated]"
	if maxChars <= len(marker) {
		return ""
	}
	end := maxChars - len(marker)
	for end > 0 && !utf8.ValidString(content[:end]) {
		end--
	}
	if end <= 0 {
		return ""
	}
	return content[:end] + marker
}
