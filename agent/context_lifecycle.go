package agentv3

import (
	"context"

	"csust-got/orm"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// RecordProcessMessage must be called only after a framework placeholder/progress send succeeds.
func RecordProcessMessage(bot *tb.Bot, message, parent *tb.Message) {
	if bot == nil || bot.Me == nil || message == nil || message.Sender == nil ||
		message.Sender.ID != bot.Me.ID || !message.Sender.IsBot {
		return
	}
	stored := responseWithParent(message, parent)
	if parent != nil && parent.Chat != nil && stored.Chat != nil && parent.Chat.ID == stored.Chat.ID &&
		stored.ReplyTo != nil && stored.ReplyTo.ID == parent.ID && stored.ReplyTo.ReplyTo == nil {
		reply := *stored.ReplyTo
		reply.ReplyTo = parent.ReplyTo
		stored.ReplyTo = &reply
	}
	if err := orm.StoreBotMessageLifecycle(stored, bot.Me.ID, false); err != nil {
		zap.L().Error("agentv3: failed to record process message", zap.Error(err))
		return
	}
	// An outbound send is not an inbound middleware event. Keep its identity in the stream's cache window.
	if err := orm.PushMessageToStream(stored); err != nil {
		zap.L().Error("agentv3: failed to cache process message", zap.Error(err))
	}
}

// resolveContextMessage keeps process ancestors available for traversal but excludes their body.
// A known final always uses the independently persisted snapshot, never the live Telegram object.
func resolveContextMessage(ctx context.Context, chatID int64, message *tb.Message) (*tb.Message, bool, error) {
	ctx, done := withContextMessageResolver(ctx)
	defer done()
	return ctx.Value(contextMessageResolverKey{}).(*contextMessageResolver).resolve(chatID, message)
}

type contextMessageResolverKey struct{}

type contextMessageIdentity struct {
	chatID int64
	id     int
}

type contextMessageCandidate struct {
	chatID  int64
	message *tb.Message
}

type contextMessageResolution struct {
	message *tb.Message
	process bool
}

type contextMessageResolver struct {
	ctx       context.Context
	reader    *orm.MessageReader
	states    map[contextMessageIdentity]*orm.BotMessageLifecycle
	canonical map[contextMessageCandidate]contextMessageResolution
	stored    map[contextMessageIdentity]*tb.Message
}

// Bind one resolver per query/build operation, not per Telegram candidate.
func withContextMessageResolver(ctx context.Context) (context.Context, func()) {
	if ctx.Value(contextMessageResolverKey{}) != nil {
		return ctx, func() {}
	}
	r := &contextMessageResolver{ctx: ctx, states: make(map[contextMessageIdentity]*orm.BotMessageLifecycle),
		canonical: make(map[contextMessageCandidate]contextMessageResolution), stored: make(map[contextMessageIdentity]*tb.Message)}
	return context.WithValue(ctx, contextMessageResolverKey{}, r), func() {
		if r.reader != nil {
			r.reader.Close()
		}
	}
}

func (r *contextMessageResolver) messageReader() *orm.MessageReader {
	if r.reader == nil {
		r.reader = orm.NewMessageReader(r.ctx)
	}
	return r.reader
}

func (r *contextMessageResolver) prime(chatID int64, messages ...*tb.Message) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	ids := make(map[int]bool)
	visited := make(map[*tb.Message]bool)
	for _, message := range messages {
		depth := 0
		for current := message; current != nil && !visited[current] && depth < replySessionAncestorLimit; current = current.ReplyTo {
			depth++
			visited[current] = true
			if current.Chat != nil && current.Chat.ID != chatID {
				break
			}
			if current.ID > 0 && (current.Sender == nil || current.Sender.IsBot) {
				if _, known := r.states[contextMessageIdentity{chatID, current.ID}]; !known {
					ids[current.ID] = true
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	requested := make([]int, 0, len(ids))
	for id := range ids {
		requested = append(requested, id)
	}
	states, err := r.messageReader().GetBotMessageLifecycles(chatID, requested)
	if err != nil {
		return err
	}
	for _, id := range requested {
		r.states[contextMessageIdentity{chatID, id}] = states[id]
	}
	return nil
}

func (r *contextMessageResolver) loadStored(chatID int64, id int) (*tb.Message, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	key := contextMessageIdentity{chatID, id}
	if state := r.states[key]; state != nil && state.Final {
		return state.Message, nil
	}
	message, known := r.stored[key]
	if !known {
		messages, err := r.messageReader().GetMessages(chatID, []int{id})
		if err != nil {
			return nil, err
		}
		message = messages[id]
		r.stored[key] = message
	}
	if message == nil {
		return nil, redis.Nil
	}
	return message, nil
}

func (r *contextMessageResolver) resolve(chatID int64, message *tb.Message) (*tb.Message, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, false, err
	}
	if message == nil || (message.Sender != nil && !message.Sender.IsBot) {
		return message, false, nil
	}
	if message.Chat != nil && message.Chat.ID != chatID {
		return message, false, nil
	}
	key := contextMessageCandidate{chatID, message}
	if cached, ok := r.canonical[key]; ok {
		return cached.message, cached.process, nil
	}
	if err := r.prime(chatID, message); err != nil {
		return nil, false, err
	}
	state := r.states[contextMessageIdentity{chatID, message.ID}]
	if state == nil || (message.Sender != nil && message.Sender.ID != state.BotID) {
		return message, false, nil
	}
	resolved := *state.Message
	if resolved.ReplyTo == nil {
		resolved.ReplyTo = message.ReplyTo
	} else if message.ReplyTo != nil && resolved.ReplyTo.ID == message.ReplyTo.ID && resolved.ReplyTo.ReplyTo == nil {
		parent := *resolved.ReplyTo
		parent.ReplyTo = message.ReplyTo.ReplyTo
		resolved.ReplyTo = &parent
	}
	r.canonical[key] = contextMessageResolution{&resolved, !state.Final}
	return &resolved, !state.Final, nil
}

// resolveContextReplyChain clones the live chain, replacing finals and bypassing process nodes.
func resolveContextReplyChain(ctx context.Context, chatID int64, message *tb.Message) (*tb.Message, error) {
	ctx, done := withContextMessageResolver(ctx)
	defer done()
	resolver := ctx.Value(contextMessageResolverKey{}).(*contextMessageResolver)
	if err := resolver.prime(chatID, message); err != nil {
		return nil, err
	}
	var head *tb.Message
	tail := &head
	visited := make(map[int]bool)
	for message != nil && len(visited) < replySessionAncestorLimit {
		if visited[message.ID] {
			break
		}
		visited[message.ID] = true
		resolved, process, err := resolveContextMessage(ctx, chatID, message)
		if err != nil {
			return nil, err
		}
		if !process {
			clone := *resolved
			clone.ReplyTo = nil
			*tail = &clone
			tail = &clone.ReplyTo
		}
		message = resolved.ReplyTo
	}
	return head, nil
}

func responseWithParent(message, parent *tb.Message) *tb.Message {
	stored := *message
	if stored.ReplyTo == nil && parent != nil && parent.Chat != nil && stored.Chat != nil &&
		stored.Chat.ID == parent.Chat.ID && parent.ID > 0 && parent.ID < stored.ID {
		clone := *parent
		clone.ReplyTo = nil
		stored.ReplyTo = &clone
	}
	return &stored
}
