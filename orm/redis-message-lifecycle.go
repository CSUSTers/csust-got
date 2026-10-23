package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	tb "gopkg.in/telebot.v3"
)

const messageCacheTTL = 24 * time.Hour

var (
	errInvalidBotMessageIdentity = errors.New("invalid bot message identity")
	errMessageCacheUnavailable   = errors.New("message cache unavailable")
)

// BotMessageLifecycle holds a trusted bot snapshot and its delivery state.
type BotMessageLifecycle struct {
	BotID   int64       `json:"bot_id"`
	Final   bool        `json:"final"`
	Message *tb.Message `json:"message"`
}

func botMessageLifecyclePrefix(chatID int64) string {
	return wrapKeyWithChat("message_bot_lifecycle", chatID) + ":u"
}

func botMessageLifecycleKeys(key string, chatID int64) []string {
	return []string{key, wrapKeyWithChat("message_bot_lifecycle_stream", chatID), wrapKeyWithChat("message_bot_lifecycle_refs", chatID)}
}

// Only candidate bot identities and shallow ID references need a lookup; ordinary user roots do not.
func lifecycleCandidateIDs(message *tb.Message) []string {
	ids := make([]string, 0)
	if message == nil || message.Chat == nil {
		return ids
	}
	chatID := message.Chat.ID
	visited := make(map[int]bool)
	for current := message; current != nil && len(visited) < 1000; current = current.ReplyTo {
		if current.ID <= 0 || visited[current.ID] || (current.Chat != nil && current.Chat.ID != chatID) {
			break
		}
		visited[current.ID] = true
		if current.Sender == nil || current.Sender.IsBot {
			ids = append(ids, strconv.Itoa(current.ID))
		}
	}
	return ids
}

// Stream roots pin a capped, flat ancestor set. Dependency copies do not recursively pin older history.
const botMessageReferencesLua = `
local lifecyclePrefix
local loaded = {}
local function readState(id)
  if loaded[id] == nil then
    loaded[id] = redis.call('GET', lifecyclePrefix .. id) or redis.call('HGET', KEYS[2], id) or false
  end
  return loaded[id]
end
local function envelope(blob)
  local value = cjson.decode(blob)
  return value, value['refs'] or {}
end
local function increment(id)
  local blob = readState(id)
  if not blob then return false end
  if redis.call('HINCRBY', KEYS[3], id, 1) == 1 then
    local value = envelope(blob)
    redis.call('HSET', KEYS[2], id, cjson.encode({data=value['data'], refs=value['refs'], held={}}))
  end
  return true
end
local function decrement(id)
  if redis.call('HINCRBY', KEYS[3], id, -1) == 0 then
    redis.call('HDEL', KEYS[2], id)
    redis.call('HDEL', KEYS[3], id)
  end
end
local function updateHeld(id, blob)
  local old = envelope(redis.call('HGET', KEYS[2], id))
  local value, refs = envelope(blob)
  local held, previous = {}, old['held'] or {}
  for child, _ in pairs(refs) do
    if previous[child] or increment(child) then held[child] = true end
  end
  redis.call('HSET', KEYS[2], id, cjson.encode({data=value['data'], refs=refs, held=held}))
  for child, _ in pairs(previous) do if not held[child] then decrement(child) end end
end
local function retain(id)
  if not increment(id) then return false end
  if redis.call('HINCRBY', KEYS[3], 'root:' .. id, 1) == 1 then updateHeld(id, readState(id)) end
  return true
end
local function release(id)
  if redis.call('HINCRBY', KEYS[3], 'root:' .. id, -1) == 0 then
    local value = envelope(redis.call('HGET', KEYS[2], id))
    for child, _ in pairs(value['held'] or {}) do decrement(child) end
    redis.call('HSET', KEYS[2], id, cjson.encode({data=value['data'], refs=value['refs'], held={}}))
    redis.call('HDEL', KEYS[3], 'root:' .. id)
  end
  decrement(id)
end
local function collectReferences(candidates, upper, previous)
  local found = previous or {}
  for _, id in ipairs(candidates) do
    if tonumber(id) < tonumber(upper) and readState(id) then
      found[id] = true
      local _, refs = envelope(readState(id))
      for child, _ in pairs(refs) do
        if tonumber(child) < tonumber(upper) then found[child] = true end
      end
    end
  end
  local ordered = {}
  for id, _ in pairs(found) do table.insert(ordered, id) end
  table.sort(ordered, function(a, b) return tonumber(a) > tonumber(b) end)
  local refs = {}
  for i = 1, math.min(#ordered, 1000) do
    local id = ordered[i]
    if readState(id) then refs[id] = true end
  end
  return refs
end
local function renewStandalone(candidates, ttl, expand)
  local ids = {}
  for _, id in ipairs(candidates) do
    if readState(id) then
      ids[id] = true
      if expand ~= false then
        local _, refs = envelope(readState(id))
        for child, _ in pairs(refs) do ids[child] = true end
      end
    end
  end
  for id, _ in pairs(ids) do
    local blob = readState(id)
    if blob then
      local value, refs = envelope(blob)
      redis.call('SET', lifecyclePrefix .. id, cjson.encode({data=value['data'], refs=refs}), 'EX', ttl)
    end
  end
end
`

var storeBotMessageLifecycleScript = redis.NewScript(botMessageReferencesLua + `
lifecyclePrefix = ARGV[5]
local id = ARGV[4]
local existing = readState(id)
local previous = {}
if existing then local _, refs = envelope(existing); previous = refs end
local refs = collectReferences(cjson.decode(ARGV[6]), id, previous)
local blob = cjson.encode({data=ARGV[1], refs=refs})
if ARGV[3] == 'process' and existing then
  local value, previous = envelope(existing)
  blob = cjson.encode({data=value['data'], refs=previous})
end
local _, dependencies = envelope(blob)
local ancestors = {}
for child, _ in pairs(dependencies) do table.insert(ancestors, child) end
renewStandalone(ancestors, ARGV[2], false)
redis.call('SET', KEYS[1], blob, 'EX', ARGV[2])
loaded[id] = blob
if redis.call('HEXISTS', KEYS[3], id) == 1 then
  if redis.call('HEXISTS', KEYS[3], 'root:' .. id) == 1 then
    updateHeld(id, blob)
  else
    local value = envelope(blob)
    redis.call('HSET', KEYS[2], id, cjson.encode({data=value['data'], refs=value['refs'], held={}}))
  end
end
return 1
`)

// StoreBotMessageLifecycle publishes a trusted identity and an independently readable final snapshot.
func StoreBotMessageLifecycle(msg *tb.Message, botID int64, final bool) error {
	if msg == nil || msg.Chat == nil || msg.ID <= 0 || msg.Sender == nil || !msg.Sender.IsBot || botID == 0 || msg.Sender.ID != botID {
		return errInvalidBotMessageIdentity
	}
	if rc == nil {
		return errMessageCacheUnavailable
	}
	data, err := json.Marshal(BotMessageLifecycle{BotID: botID, Final: final, Message: msg})
	if err != nil {
		return err
	}
	candidates, err := json.Marshal(lifecycleCandidateIDs(msg))
	if err != nil {
		return err
	}
	state := "process"
	if final {
		state = "final"
	}
	return storeBotMessageLifecycleScript.Run(context.Background(), rc,
		botMessageLifecycleKeys(wrapKeyWithChatMsg("message_bot_lifecycle", msg.Chat.ID, msg.ID), msg.Chat.ID),
		data, int64(messageCacheTTL.Seconds()), state, strconv.Itoa(msg.ID), botMessageLifecyclePrefix(msg.Chat.ID), candidates).Err()
}

// GetBotMessageLifecycle uses one operation-scoped reader; batch callers should reuse MessageReader.
func GetBotMessageLifecycle(ctx context.Context, chatID int64, messageID int) (*BotMessageLifecycle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rc == nil {
		return nil, nil
	}
	reader := NewMessageReader(ctx)
	defer reader.Close()
	states, err := reader.GetBotMessageLifecycles(chatID, []int{messageID})
	return states[messageID], err
}

// GetBotMessageLifecycles loads recorded identities in a batch, omitting unknown messages.
func (r *MessageReader) GetBotMessageLifecycles(chatID int64, messageIDs []int) (map[int]*BotMessageLifecycle, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	states := make(map[int]*BotMessageLifecycle, len(messageIDs))
	if len(messageIDs) == 0 {
		return states, nil
	}
	keys := make([]string, len(messageIDs))
	for i, id := range messageIDs {
		keys[i] = wrapKeyWithChatMsg("message_bot_lifecycle", chatID, id)
	}
	values, err := r.client.MGet(r.ctx, keys...).Result()
	if r.ctx.Err() != nil {
		return nil, r.ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	missing, fields := make([]int, 0), make([]string, 0)
	for i, id := range messageIDs {
		if values[i] == nil {
			missing = append(missing, i)
			fields = append(fields, strconv.Itoa(id))
		}
	}
	if len(missing) > 0 {
		active, err := r.client.HMGet(r.ctx, wrapKeyWithChat("message_bot_lifecycle_stream", chatID), fields...).Result()
		if r.ctx.Err() != nil {
			return nil, r.ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		for i, index := range missing {
			values[index] = active[i]
		}
	}
	for i, id := range messageIDs {
		value := values[i]
		if value == nil {
			continue
		}
		blob, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%w: bot lifecycle envelope", ErrInvalidCachedMessage)
		}
		state, err := decodeBotMessageLifecycle([]byte(blob), chatID, id)
		if err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, nil
}

func decodeBotMessageLifecycle(blob []byte, chatID int64, messageID int) (*BotMessageLifecycle, error) {
	var envelope struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(blob, &envelope); err != nil {
		return nil, fmt.Errorf("%w: bot lifecycle envelope", ErrInvalidCachedMessage)
	}
	var stored struct {
		BotID   int64           `json:"bot_id"`
		Final   bool            `json:"final"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(envelope.Data), &stored); err != nil {
		return nil, fmt.Errorf("%w: bot lifecycle", ErrInvalidCachedMessage)
	}
	message, err := decodeCachedMessage(stored.Message)
	if err != nil {
		return nil, err
	}
	if message.Chat == nil || message.Chat.ID != chatID || message.ID != messageID || message.Sender == nil ||
		!message.Sender.IsBot || stored.BotID == 0 || message.Sender.ID != stored.BotID {
		return nil, fmt.Errorf("%w: bot lifecycle identity", ErrInvalidCachedMessage)
	}
	return &BotMessageLifecycle{BotID: stored.BotID, Final: stored.Final, Message: message}, nil
}
