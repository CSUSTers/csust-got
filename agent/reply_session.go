package agentv3

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"csust-got/orm"

	"github.com/redis/go-redis/v9"
	tb "gopkg.in/telebot.v3"
)

const replySessionAncestorLimit = 1000

var (
	errReplyChainNoCurrentMessage   = errors.New("reply_chain requires a current message")
	errReplyChainNoCurrentBlock     = errors.New("reply_chain requires a current message block")
	errReplyChainEmptySession       = errors.New("reply_chain did not produce a current message")
	errReplyChainTextBudgetTooSmall = errors.New("reply_chain text budget too small for current message metadata")
)

type replySessionBlock struct {
	Messages []*tb.Message
	Current  bool
}

type replySession struct {
	Blocks     []replySessionBlock
	Incomplete bool
	Truncated  bool
}

func loadReplySession(ctx context.Context, current *tb.Message, maxContext int) (replySession, error) {
	if err := ctx.Err(); err != nil {
		return replySession{}, err
	}
	if current == nil || current.Chat == nil || current.ID <= 0 {
		return replySession{Incomplete: true}, nil
	}

	nearby, err := orm.GetMessagesFromStream(current.Chat.ID, strconv.Itoa(current.ID), "-", replySessionAncestorLimit, true)
	if err != nil && !errors.Is(err, redis.Nil) {
		return replySession{}, fmt.Errorf("load reply session messages: %w", err)
	}
	if errors.Is(err, redis.Nil) {
		nearby = nil
	}

	return selectReplySession(ctx, current, nearby, orm.GetMessage, maxContext)
}

func selectReplySession(ctx context.Context, current *tb.Message, nearby []*tb.Message, lookup func(int64, int) (*tb.Message, error), maxContext int) (replySession, error) {
	if err := ctx.Err(); err != nil {
		return replySession{}, err
	}
	if current == nil || current.Chat == nil || current.ID <= 0 {
		return replySession{Incomplete: true}, nil
	}

	nearbyByID := make(map[int]*tb.Message, len(nearby))
	messagesByID := make(map[int]*tb.Message, len(nearby)+1)
	for _, message := range nearby {
		if !isReplySessionNearbyMessage(message, current) || message.ID == current.ID {
			continue
		}
		if _, exists := nearbyByID[message.ID]; exists {
			continue
		}
		nearbyByID[message.ID] = message
		messagesByID[message.ID] = message
	}
	messagesByID[current.ID] = current

	anchors := map[int]bool{current.ID: true}
	incomplete, err := collectReplySessionAncestors(ctx, current, nearbyByID, messagesByID, anchors, lookup)
	if err != nil {
		return replySession{}, err
	}
	if len(nearby) >= replySessionAncestorLimit {
		incomplete = true
	}
	if err := ctx.Err(); err != nil {
		return replySession{}, err
	}

	messages := make([]*tb.Message, 0, len(messagesByID))
	for _, message := range messagesByID {
		messages = append(messages, message)
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ID < messages[j].ID
	})

	blocks := selectReplySessionBlocks(messages, anchors, current.ID)
	blocks, truncated := limitReplySessionBlocks(blocks, maxContext)
	return replySession{Blocks: blocks, Incomplete: incomplete, Truncated: truncated}, nil
}

func isReplySessionNearbyMessage(message, current *tb.Message) bool {
	return message != nil && message.Chat != nil && current != nil && current.Chat != nil &&
		message.Chat.ID == current.Chat.ID && message.ID > 0 && message.ID <= current.ID
}

func collectReplySessionAncestors(ctx context.Context, current *tb.Message, nearbyByID, messagesByID map[int]*tb.Message, anchors map[int]bool, lookup func(int64, int) (*tb.Message, error)) (bool, error) {
	parent := current.ReplyTo
	previousID := current.ID
	visited := map[int]bool{current.ID: true}
	ancestorCount := 0
	incomplete := false

	for parent != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		normalizedParent, valid := replySessionEmbeddedParent(parent, current.Chat)
		if !valid || normalizedParent.ID >= previousID || visited[normalizedParent.ID] {
			return true, nil
		}
		if ancestorCount >= replySessionAncestorLimit {
			return true, nil
		}

		visited[normalizedParent.ID] = true
		ancestorCount++
		resolved, fullRecord, err := lookupReplySessionParent(ctx, current.Chat, normalizedParent, nearbyByID, lookup)
		if err != nil {
			return false, err
		}
		if resolved == nil {
			return true, nil
		}
		if resolved.ID != normalizedParent.ID || resolved.Chat == nil || resolved.Chat.ID != current.Chat.ID {
			return true, nil
		}
		if contextMessageFromTelegram(resolved) == nil {
			return true, nil
		}

		messagesByID[resolved.ID] = resolved
		anchors[resolved.ID] = true
		previousID = resolved.ID
		if !fullRecord {
			incomplete = true
		}
		parent = resolved.ReplyTo
	}

	return incomplete, nil
}

func lookupReplySessionParent(ctx context.Context, chat *tb.Chat, embedded *tb.Message, nearbyByID map[int]*tb.Message, lookup func(int64, int) (*tb.Message, error)) (*tb.Message, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if lookup != nil {
		stored, err := lookup(chat.ID, embedded.ID)
		if err == nil && stored != nil {
			if stored.Chat == nil || stored.Chat.ID != chat.ID {
				return nil, false, nil
			}
			return stored, true, nil
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, false, fmt.Errorf("load reply parent %d: %w", embedded.ID, err)
		}
	}

	if nearby := nearbyByID[embedded.ID]; nearby != nil {
		if nearby.ReplyTo == nil && embedded.ReplyTo != nil {
			clone := *nearby
			clone.ReplyTo = embedded.ReplyTo
			return &clone, false, nil
		}
		return nearby, false, nil
	}
	if contextMessageFromTelegram(embedded) != nil {
		return embedded, false, nil
	}
	return nil, false, nil
}

func replySessionEmbeddedParent(message *tb.Message, chat *tb.Chat) (*tb.Message, bool) {
	if message == nil || message.ID <= 0 || chat == nil {
		return nil, false
	}
	if message.Chat != nil {
		return message, message.Chat.ID == chat.ID
	}
	clone := *message
	clone.Chat = chat
	return &clone, true
}

func canJoinReplyUtterances(a, b *tb.Message) bool {
	if a == nil || b == nil || a.Chat == nil || b.Chat == nil ||
		a.Sender == nil || b.Sender == nil || a.Sender.ID == 0 ||
		a.Sender.ID != b.Sender.ID || a.Sender.IsBot || b.Sender.IsBot ||
		a.Chat.ID != b.Chat.ID || b.ID != a.ID+1 {
		return false
	}
	if contextMessageFromTelegram(a) == nil || contextMessageFromTelegram(b) == nil {
		return false
	}
	gap := b.Unixtime - a.Unixtime
	return gap >= 0 && gap < 60
}

type replySessionBlockBuilder struct {
	messages       []*tb.Message
	memberIDs      map[int]bool
	replyTarget    int
	hasReplyTarget bool
}

func newReplySessionBlockBuilder(message *tb.Message) *replySessionBlockBuilder {
	builder := &replySessionBlockBuilder{memberIDs: make(map[int]bool)}
	builder.append(message)
	return builder
}

func (builder *replySessionBlockBuilder) canAppend(message *tb.Message) bool {
	last := builder.messages[len(builder.messages)-1]
	if !canJoinReplyUtterances(last, message) {
		return false
	}
	if message.ReplyTo == nil {
		return true
	}
	target, valid := replySessionReplyTarget(message)
	if !valid {
		return false
	}
	if builder.hasReplyTarget && target == builder.replyTarget {
		return true
	}
	return builder.memberIDs[target]
}

func (builder *replySessionBlockBuilder) append(message *tb.Message) {
	builder.messages = append(builder.messages, message)
	builder.memberIDs[message.ID] = true
	if target, valid := replySessionReplyTarget(message); valid && !builder.hasReplyTarget {
		builder.replyTarget = target
		builder.hasReplyTarget = true
	}
}

func replySessionReplyTarget(message *tb.Message) (int, bool) {
	if message == nil || message.ReplyTo == nil || message.ReplyTo.ID <= 0 || message.ReplyTo.ID >= message.ID {
		return 0, false
	}
	return message.ReplyTo.ID, true
}

func selectReplySessionBlocks(messages []*tb.Message, anchors map[int]bool, currentID int) []replySessionBlock {
	if len(messages) == 0 {
		return nil
	}

	allBlocks := make([]replySessionBlock, 0, len(messages))
	builder := newReplySessionBlockBuilder(messages[0])
	for _, message := range messages[1:] {
		if builder.canAppend(message) {
			builder.append(message)
			continue
		}
		allBlocks = append(allBlocks, replySessionBlock{Messages: builder.messages})
		builder = newReplySessionBlockBuilder(message)
	}
	allBlocks = append(allBlocks, replySessionBlock{Messages: builder.messages})

	blocks := make([]replySessionBlock, 0, len(allBlocks))
	for _, block := range allBlocks {
		include := false
		for _, message := range block.Messages {
			if anchors[message.ID] {
				include = true
			}
			if message.ID == currentID {
				block.Current = true
			}
		}
		if include {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func limitReplySessionBlocks(blocks []replySessionBlock, maxContext int) ([]replySessionBlock, bool) {
	if maxContext <= 0 {
		maxContext = defaultHistoryContext
	}
	if len(blocks) == 0 {
		return blocks, false
	}

	historyMessages := 0
	first := len(blocks) - 1
	for index := len(blocks) - 1; index >= 0; index-- {
		block := blocks[index]
		first = index
		historyMessages += len(block.Messages)
		if block.Current {
			historyMessages--
		}
		if historyMessages >= maxContext {
			break
		}
	}
	return blocks[first:], first > 0
}
