package agentv3

import (
	"unicode/utf16"
	"unicode/utf8"

	tb "gopkg.in/telebot.v3"
)

const replySessionDefaultTextBudget = 6000 * 4

func replySessionLocalBlocks(session replySession, current *tb.Message) []replySessionBlock {
	blocks := make([]replySessionBlock, 0, len(session.Blocks))
	for _, block := range session.Blocks {
		local := replySessionBlock{Current: block.Current, Messages: make([]*tb.Message, 0, len(block.Messages))}
		for _, message := range block.Messages {
			if message != nil {
				local.Messages = append(local.Messages, replySessionMessageCopy(message))
			}
		}
		if len(local.Messages) > 0 {
			blocks = append(blocks, local)
		}
	}
	if len(blocks) == 0 && current != nil {
		blocks = append(blocks, replySessionBlock{Messages: []*tb.Message{replySessionMessageCopy(current)}, Current: true})
	}
	return blocks
}

func limitReplySessionBlocksByText(blocks []replySessionBlock, tc *TurnContext, incomplete, truncated bool, maxChars int) ([]replySessionBlock, bool, error) {
	if maxChars <= 0 {
		maxChars = replySessionDefaultTextBudget
	}
	currentIndex := replySessionCurrentBlockIndex(blocks, tc.Message.ID)
	if currentIndex < 0 {
		return nil, false, errReplyChainNoCurrentBlock
	}
	budgetOmitted := false
	for replySessionRenderedTextLength(blocks, tc, incomplete, truncated || budgetOmitted, currentIndex) > maxChars {
		removed := false
		for index := range blocks {
			if index == currentIndex {
				continue
			}
			blocks = append(blocks[:index], blocks[index+1:]...)
			if index < currentIndex {
				currentIndex--
			}
			budgetOmitted, removed = true, true
			break
		}
		if removed {
			continue
		}
		if replySessionRemoveOldestCurrentMember(&blocks[currentIndex], tc.Message.ID) {
			budgetOmitted = true
			continue
		}
		budgetOmitted = true
		if err := truncateReplySessionCurrentMessage(blocks, currentIndex, tc, incomplete, truncated || budgetOmitted, maxChars); err != nil {
			return nil, false, err
		}
		break
	}
	return blocks, budgetOmitted, nil
}

func replySessionCurrentBlockIndex(blocks []replySessionBlock, currentID int) int {
	for index := len(blocks) - 1; index >= 0; index-- {
		if blocks[index].Current {
			return index
		}
	}
	for index := len(blocks) - 1; index >= 0; index-- {
		for _, message := range blocks[index].Messages {
			if message != nil && message.ID == currentID {
				return index
			}
		}
	}
	return -1
}

func replySessionRemoveOldestCurrentMember(block *replySessionBlock, triggerID int) bool {
	if block == nil || len(block.Messages) <= 1 {
		return false
	}
	for index, message := range block.Messages {
		if message == nil || message.ID != triggerID {
			block.Messages = append(block.Messages[:index], block.Messages[index+1:]...)
			return true
		}
	}
	return false
}

func replySessionRenderedTextLength(blocks []replySessionBlock, tc *TurnContext, incomplete, truncated bool, currentIndex int) int {
	total := 0
	for index, block := range blocks {
		total += len(replySessionRenderedBlockText(block, tc, incomplete && index == currentIndex, truncated && index == currentIndex))
	}
	return total
}

func truncateReplySessionCurrentMessage(blocks []replySessionBlock, currentIndex int, tc *TurnContext, incomplete, truncated bool, maxChars int) error {
	if currentIndex < 0 || currentIndex >= len(blocks) {
		return errReplyChainNoCurrentBlock
	}
	block := &blocks[currentIndex]
	messageIndex := len(block.Messages) - 1
	for index, message := range block.Messages {
		if message != nil && message.ID == tc.Message.ID {
			messageIndex = index
			break
		}
	}
	if messageIndex < 0 || block.Messages[messageIndex] == nil {
		return errReplyChainNoCurrentMessage
	}
	original := block.Messages[messageIndex]
	best := replySessionMessageWithSuffix(original, 0)
	block.Messages[messageIndex] = best
	if replySessionRenderedTextLength(blocks, tc, incomplete, truncated, currentIndex) > maxChars {
		return errReplyChainTextBudgetTooSmall
	}

	low, high := 0, len(replySessionRawMessageText(original))
	for low <= high {
		keep := low + (high-low)/2
		candidate := replySessionMessageWithSuffix(original, keep)
		block.Messages[messageIndex] = candidate
		if replySessionRenderedTextLength(blocks, tc, incomplete, truncated, currentIndex) <= maxChars {
			best, low = candidate, keep+1
		} else {
			high = keep - 1
		}
	}
	block.Messages[messageIndex] = best
	return nil
}

func replySessionMessageCopy(message *tb.Message) *tb.Message {
	clone := *message
	if message.ReplyTo != nil {
		clone.ReplyTo = &tb.Message{ID: message.ReplyTo.ID}
	} else {
		clone.ReplyTo = nil
	}
	clone.AlbumID = ""
	return &clone
}

func replySessionRawMessageText(message *tb.Message) string {
	if message == nil {
		return ""
	}
	if message.Text != "" {
		return message.Text
	}
	return message.Caption
}

func replySessionMessageWithSuffix(message *tb.Message, maxBytes int) *tb.Message {
	clone := replySessionMessageCopy(message)
	text := replySessionRawMessageText(clone)
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(text) <= maxBytes {
		return clone
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	entities, caption := clone.Entities, clone.Text == ""
	if caption {
		entities = clone.CaptionEntities
	}
	start = replySessionSuffixStartAfterEntities(text, entities, start)
	cutUTF16 := replySessionUTF16Length(text[:start])
	trimmed := replySessionSuffixEntities(entities, cutUTF16, replySessionUTF16Length(text))
	if caption {
		clone.Caption, clone.CaptionEntities = text[start:], trimmed
	} else {
		clone.Text, clone.Entities = text[start:], trimmed
	}
	return clone
}

func replySessionSuffixStartAfterEntities(text string, entities []tb.MessageEntity, start int) int {
	for {
		cutUTF16 := replySessionUTF16Length(text[:start])
		advanced := false
		for _, entity := range entities {
			if replySessionEntityIsAtomic(entity) && entity.Offset < cutUTF16 && cutUTF16 < entity.Offset+entity.Length {
				start, advanced = replySessionByteOffsetForUTF16(text, entity.Offset+entity.Length), true
				break
			}
		}
		if !advanced || start >= len(text) {
			return min(start, len(text))
		}
	}
}

func replySessionSuffixEntities(entities []tb.MessageEntity, cut, total int) []tb.MessageEntity {
	trimmed := make([]tb.MessageEntity, 0, len(entities))
	for _, entity := range entities {
		start, end := entity.Offset, entity.Offset+entity.Length
		if entity.Length <= 0 || start < 0 || start >= total || end <= cut {
			continue
		}
		if replySessionEntityIsAtomic(entity) {
			if start < cut || end > total {
				continue
			}
		} else {
			start = max(start, cut)
			end = min(end, total)
			if start >= end {
				continue
			}
		}
		entity.Offset, entity.Length = start-cut, end-start
		trimmed = append(trimmed, entity)
	}
	return trimmed
}

func replySessionEntityIsAtomic(entity tb.MessageEntity) bool {
	switch entity.Type {
	case tb.EntityTextLink, tb.EntityURL, tb.EntityEmail, tb.EntityPhone, tb.EntityMention,
		tb.EntityTMention, tb.EntityHashtag, tb.EntityCashtag, tb.EntityCommand, tb.EntityCustomEmoji:
		return true
	default:
		return entity.CustomEmoji != ""
	}
}

func replySessionUTF16Length(text string) int {
	length := 0
	for _, r := range text {
		length += utf16.RuneLen(r)
	}
	return length
}

func replySessionByteOffsetForUTF16(text string, offset int) int {
	runes := []rune(text)
	return len(string(runes[:utf16OffsetToRuneIndex(runes, offset)]))
}
