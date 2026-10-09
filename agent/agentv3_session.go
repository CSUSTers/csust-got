package agentv3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const agentV3SessionCommitTimeout = 10 * time.Second

var (
	agentSessionService                   atomic.Pointer[agentV3SessionService]
	errAgentV3SessionBaseline             = errors.New("incomplete or changed session invocation baseline")
	errAgentV3SessionModelMessagesChanged = errors.New("model session baseline changed non-system messages")
	errAgentV3SessionModelMessagesOmitted = errors.New("model session baseline omitted input messages")
	errAgentV3SessionContextOverflow      = errors.New("session context overflow rebuild")
	errAgentV3SessionMemoryEpoch          = errors.New("session memory deleted since node commit")
)

type agentV3SessionService struct {
	service   *session.Service
	compactor *agentV3SessionCompactor
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
}

type agentV3SessionInputKind uint8

const (
	agentV3SessionFrame agentV3SessionInputKind = iota
	agentV3SessionHistory
	agentV3SessionCurrent
)

type agentV3SessionTurn struct {
	service     *session.Service
	scope       session.Scope
	runID       string
	parent      *session.LoadedParent
	replay      []*schema.Message
	capture     *SessionCapture
	input       []*schema.Message
	kinds       []agentV3SessionInputKind
	baselineErr error
	selection   session.Selection
	memoryEpoch int64
	load        bool
	save        bool
	ownsHistory bool
	committed   bool
}

func initAgentV3SessionService(ctx context.Context) error {
	closeAgentV3SessionService()
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return nil
	}
	cfg := config.BotConfig.AgentV3.Session
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("agentv3: session configuration: %w", err)
	}
	if !cfg.Enabled() || !hasEnabledAgent() {
		return nil
	}
	repo, err := orm.NewProductionAgentV3SessionRepository()
	if err != nil {
		zap.L().Warn("agentv3: session unavailable; using legacy context", zap.String("stage", "repository"), zap.Error(err))
		return nil
	}
	files, err := session.NewFileStore(cfg.DirectoryPath())
	if err != nil {
		zap.L().Warn("agentv3: session unavailable; using legacy context", zap.String("stage", "directory"), zap.Error(err))
		return nil
	}
	service, err := session.NewService(repo, files, session.Options{TTL: cfg.IdleTTL()})
	if err != nil {
		err = errors.Join(err, files.Close())
		zap.L().Warn("agentv3: session unavailable; using legacy context", zap.String("stage", "service"), zap.Error(err))
		return nil
	}
	s := startAgentV3SessionMaintenance(ctx, service, time.Local)
	s.compactor = newAgentV3SessionCompactor(ctx, service, cfg.Compact)
	if old := agentSessionService.Swap(s); old != nil {
		old.close()
	}
	return nil
}

func closeAgentV3SessionService() {
	if old := agentSessionService.Swap(nil); old != nil {
		old.close()
	}
}

func startAgentV3SessionMaintenance(ctx context.Context, service *session.Service, location *time.Location) *agentV3SessionService {
	ctx, cancel := context.WithCancel(ctx)
	s := &agentV3SessionService{service: service, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		nextCollection, err := session.NextCollection(time.Now(), location)
		if err != nil {
			zap.L().Warn("agentv3: session maintenance schedule failed", zap.Error(err))
			return
		}
		s.recover(ctx)
		if s.catchUpDue(ctx, time.Now(), location) {
			zap.L().Info("agentv3: running catch-up session collection after missed daily schedule")
			nextCollection, err = s.collectAndSchedule(ctx, location)
			if err != nil {
				zap.L().Warn("agentv3: session maintenance schedule failed", zap.Error(err))
				return
			}
		}
		for ctx.Err() == nil {
			next := time.Now().Add(time.Minute)
			collect := !next.Before(nextCollection)
			if collect {
				next = nextCollection
			}
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				if collect {
					nextCollection, err = s.collectAndSchedule(ctx, location)
					if err != nil {
						zap.L().Warn("agentv3: session maintenance schedule failed", zap.Error(err))
						return
					}
				} else {
					s.recover(ctx)
				}
			}
		}
	}()
	return s
}

func (s *agentV3SessionService) recover(ctx context.Context) {
	err := s.service.Recover(ctx)
	if err != nil && ctx.Err() == nil {
		zap.L().Warn("agentv3: session recovery failed; will retry", zap.Error(err))
	}
}

func (s *agentV3SessionService) collectAndSchedule(ctx context.Context, location *time.Location) (time.Time, error) {
	ok := s.collect(ctx, location)
	now := time.Now()
	next, err := session.NextCollection(now, location)
	if err == nil && !ok {
		if retry := now.Add(time.Hour); retry.Before(next) {
			next = retry
		}
	}
	return next, err
}

func (s *agentV3SessionService) collect(ctx context.Context, location *time.Location) bool {
	if err := s.service.Collect(ctx); err != nil {
		if ctx.Err() == nil {
			zap.L().Warn("agentv3: session collection failed; retrying within the hour", zap.Error(err))
		}
		return false
	}
	err := s.service.MarkCollection(ctx, agentV3SessionCollectionDay(time.Now(), location))
	if err != nil && !errors.Is(err, errors.ErrUnsupported) && ctx.Err() == nil {
		zap.L().Warn("agentv3: session collection marker was not saved", zap.Error(err))
	}
	return true
}

func (s *agentV3SessionService) catchUpDue(ctx context.Context, now time.Time, location *time.Location) bool {
	last, err := s.service.LastCollection(ctx)
	if err != nil {
		if !errors.Is(err, errors.ErrUnsupported) && ctx.Err() == nil {
			zap.L().Warn("agentv3: session collection marker unreadable; assuming no collection today", zap.Error(err))
		}
		last = ""
	}
	return agentV3SessionCatchUpDue(now, location, last)
}

func agentV3SessionCollectionDay(now time.Time, location *time.Location) string {
	if location == nil {
		location = time.Local
	}
	return now.In(location).Format(time.DateOnly)
}

// agentV3SessionCatchUpDue reports whether today's 02:00 collection already passed without running.
func agentV3SessionCatchUpDue(now time.Time, location *time.Location, lastCollection string) bool {
	next, err := session.NextCollection(now, location)
	if err != nil {
		return false
	}
	today := agentV3SessionCollectionDay(now, location)
	return agentV3SessionCollectionDay(next, location) != today && lastCollection != today
}

func (s *agentV3SessionService) close() {
	s.once.Do(func() {
		s.cancel()
		if s.compactor != nil {
			s.compactor.close()
		}
		if err := s.service.Close(); err != nil {
			zap.L().Warn("agentv3: session close failed", zap.Error(err))
		}
		<-s.done
	})
}

func setupAgentV3SessionTurn(tc *TurnContext) {
	if tc.Background {
		return
	}
	state := &agentV3SessionTurn{scope: session.Scope{Bot: agentV3BotName(tc), Platform: agentV3Platform, ChatID: tc.ChatID}, ownsHistory: true}
	tc.Session = state
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil || !config.BotConfig.AgentV3.Enable || !config.BotConfig.AgentV3.Session.Enabled() {
		return
	}
	s := agentSessionService.Load()
	if s == nil {
		return
	}
	save, load := tc.Config.EffectiveSessionSettings(tc.Trigger)
	replyTarget := agentV3SessionBotReplyTarget(tc)
	if replyTarget != nil {
		save, load = true, true
	}
	state.service = s.service
	if save {
		state.runID, state.baselineErr = session.NewID()
	}
	if save || load {
		// Load-only turns also capture so a context-limit error can be attributed to the first model call.
		state.capture = NewSessionCapture()
	}
	state.save = save
	if !load {
		return
	}
	selection := session.Selection{Scope: state.scope, Agent: tc.Config.Name, Mode: session.SelectLatest, ContextKey: agentV3SessionContextKey(tc.Config), LoadOnly: !save}
	switch {
	case replyTarget != nil:
		selection.Mode, selection.ReplyMessageID = session.SelectReply, replyTarget.ID
	case tc.Trigger != nil && tc.Trigger.Reply:
		selection.Mode = session.SelectReply
		if target, valid := replySessionEmbeddedParent(tc.Message.ReplyTo, tc.Message.Chat); valid && target.Chat.ID == tc.ChatID {
			selection.ReplyMessageID = target.ID
		}
	}
	state.selection, state.load = selection, true
}

// agentV3SessionBotReplyTarget returns the replied bot message when the current message
// answers this bot directly, regardless of which trigger kind selected the agent.
func agentV3SessionBotReplyTarget(tc *TurnContext) *tb.Message {
	if tc == nil || tc.Message == nil || tc.Message.ReplyTo == nil || tc.BotUser == nil || tc.BotUser.ID == 0 {
		return nil
	}
	replied := tc.Message.ReplyTo
	if replied.Sender == nil || replied.Sender.ID != tc.BotUser.ID {
		return nil
	}
	target, valid := replySessionEmbeddedParent(replied, tc.Message.Chat)
	if !valid || target.Chat.ID != tc.ChatID {
		return nil
	}
	return target
}

func closeAgentV3SessionTurn(tc *TurnContext) {
	if tc.Session != nil && tc.Session.parent != nil {
		if err := tc.Session.parent.Close(); err != nil {
			zap.L().Warn("agentv3: session parent release failed", zap.Error(err))
		}
	}
}

type agentV3SessionReplyProof interface{ ContainsReplyMessageID(int) bool }

type agentV3PreparedSessionInput struct {
	messages     []*schema.Message
	frameIndexes []int
	currentStart int
	imageRefs    []orm.AgentV3ImageRef
}

func buildAgentV3SessionInput(cc *CompiledAgent, tc *TurnContext, proof agentV3SessionReplyProof) ([]*schema.Message, error) {
	current := *tc.Message
	if target, valid := replySessionEmbeddedParent(current.ReplyTo, current.Chat); valid && current.Chat.ID == tc.ChatID {
		if proof != nil && proof.ContainsReplyMessageID(target.ID) {
			current.ReplyTo = nil
		} else {
			quote := *target
			quote.ReplyTo = nil
			current.ReplyTo = &quote
		}
	} else {
		current.ReplyTo = nil
	}
	currentTC := &TurnContext{Bot: tc.Bot, BotUser: tc.BotUser, Message: &current, ChatID: tc.ChatID, Config: tc.Config, Trigger: tc.Trigger, V3: tc.V3}
	if !tc.Config.UsesReplyChain() {
		history := &RichHistory{}
		if current.ReplyTo != nil {
			quote := contextMessageFromTelegram(current.ReplyTo)
			if quote == nil {
				quote = &ContextMessage{ID: current.ReplyTo.ID}
			}
			history.ContextMessages = []*ContextMessage{quote}
		}
		quoted, err := agentV3SessionTemplateQuotesReply(cc, currentTC, history)
		if err != nil {
			return nil, err
		}
		message, err := buildAgentV3UserMessage(cc, currentTC, history, nil)
		if err != nil {
			return nil, err
		}
		if current.ReplyTo != nil && !quoted {
			return []*schema.Message{schema.UserMessage(FormatSingleTbMessage(current.ReplyTo, "reply_to_message")), message}, nil
		}
		return []*schema.Message{message}, nil
	}
	currentMessages, err := buildReplySessionMessages(cc, currentTC, replySession{Blocks: []replySessionBlock{{Messages: []*tb.Message{&current}, Current: true}}}, 0)
	if err != nil {
		return nil, err
	}
	addition, err := buildReplySessionPromptAddition(cc, tc)
	if err != nil {
		return nil, err
	}
	messages := []*schema.Message{addition}
	if current.ReplyTo != nil {
		quoteTarget := *current.ReplyTo
		quoteTarget.AlbumID = ""
		quoteTC := replySessionEncodingContext(currentTC, []*tb.Message{&quoteTarget})
		quote := buildUserMessage("<current_user_quote>\n"+replySessionRenderedMessageText(current.ReplyTo)+"\n</current_user_quote>", quoteTC, nil)
		messages = append(messages, quote)
		tc.V3.ImageRefs = normalizeAgentV3ImageRefs(append(tc.V3.ImageRefs, replySessionMessageImageRefs(current.ReplyTo)...))
	}
	return append(messages, currentMessages...), nil
}

func agentV3SessionTemplateQuotesReply(cc *CompiledAgent, tc *TurnContext, history *RichHistory) (bool, error) {
	if cc.PromptTemplate == nil || tc.Message.ReplyTo == nil {
		return false, nil
	}
	// Probe template data, not user-authored XML: only an emitted reply field can suppress the extra reference.
	marker := newAgentV3RunID()
	quoted := *history.ContextMessages[0]
	quoted.Text += marker
	pd := buildPromptData(tc, []*ContextMessage{&quoted})
	pd.ReplyToXml = strings.TrimSuffix(pd.ReplyToXml, "</reply_to_message>") + marker + "</reply_to_message>"
	var rendered bytes.Buffer
	if err := cc.PromptTemplate.Execute(&rendered, pd); err != nil {
		return false, fmt.Errorf("failed to render session reply template: %w", err)
	}
	return strings.Contains(rendered.String(), marker), nil
}

func agentV3SessionContextKey(cfg *config.AgentConfig) string {
	var model *config.Model
	if cfg != nil {
		model = cfg.Model
	}
	if config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
		model = config.BotConfig.AgentV3.EffectiveModel(model)
	}
	identity := []string{"session-context-v1"}
	if cfg != nil {
		identity = append(identity, cfg.Name)
	}
	if model != nil {
		identity = append(identity, model.Name, model.Model, model.BaseUrl)
	}
	return hashString(strings.Join(identity, "\x00"))
}

// rejectAgentV3SessionContext reports provider context-limit failures. The loaded node is
// only marked rejected when the capture proves the first model call of the turn failed; later
// failures stem from this turn's own tool growth, not from the restored history. Without a
// capture that cannot be proven, so the node is kept.
func rejectAgentV3SessionContext(tc *TurnContext, err error) bool {
	if !isAgentV3ProviderContextLimit(err) {
		return false
	}
	if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
		setAgentV3ContextLimitTraceError(tc.V3.Trace, err)
	}
	if tc == nil || tc.Session == nil || tc.Session.parent == nil {
		return true
	}
	state := tc.Session
	if state.capture == nil {
		zap.L().Debug("agentv3: provider context limit without session capture; loaded context kept")
		return true
	}
	if state.capture.ModelResponses() == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), agentV3SessionCommitTimeout)
		defer cancel()
		if markErr := state.service.RejectContext(ctx, state.scope, state.parent.Ref(), state.selection.ContextKey); markErr != nil {
			zap.L().Warn("agentv3: provider context rejection could not be saved")
		} else {
			zap.L().Debug("agentv3: provider rejected session context; next invocation uses fallback")
		}
	}
	return true
}

// buildAgentV3LoadedInput replays the loaded history verbatim after the current system
// prefix so the provider prompt cache can align on the archived byte sequence. Only the
// system message is rebuilt; replayed memory snapshots are never rewritten. When the current
// memory differs from the last replayed snapshot, a superseding snapshot (or an explicit
// cleared marker) is appended so the model stops trusting the stale text.
func buildAgentV3LoadedInput(cc *CompiledAgent, tc *TurnContext, prefix, memory string, replay []*schema.Message, proof agentV3SessionReplyProof) (agentV3PreparedSessionInput, error) {
	current, err := buildAgentV3SessionInput(cc, tc, proof)
	if err != nil {
		return agentV3PreparedSessionInput{}, err
	}
	prepared := agentV3PreparedSessionInput{messages: []*schema.Message{schema.SystemMessage(prefix)}, frameIndexes: []int{0}}
	prepared.messages = append(prepared.messages, replay...)
	prepared.currentStart = len(prepared.messages)
	if message := agentV3MemorySnapshotDelta(replay, memory); message != nil {
		prepared.messages = append(prepared.messages, message)
	}
	prepared.messages = append(prepared.messages, current...)
	prepared.imageRefs = tc.V3.ImageRefs
	return prepared, nil
}

// agentV3MemorySnapshotDelta returns the snapshot message to append after the replay, or nil
// when the replayed history already ends in the current memory state.
func agentV3MemorySnapshotDelta(replay []*schema.Message, memory string) *schema.Message {
	memory = strings.TrimSpace(memory)
	last, found := agentV3ReplayLastMemorySnapshot(replay)
	switch {
	case !found:
		return buildAgentV3MemorySnapshotMessage(memory)
	case last == memory:
		return nil
	default:
		return buildAgentV3MemorySnapshotUpdate(memory)
	}
}

// agentV3ReplayLastMemorySnapshot returns the body of the latest snapshot in the replay.
func agentV3ReplayLastMemorySnapshot(replay []*schema.Message) (string, bool) {
	for i := len(replay) - 1; i >= 0; i-- {
		if body, ok := agentV3MemorySnapshotBody(replay[i]); ok {
			return body, true
		}
	}
	return "", false
}

func isPureAgentV3SessionError(err, sentinel error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isPureAgentV3SessionError(child, sentinel) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return isPureAgentV3SessionError(wrapped.Unwrap(), sentinel)
	}
	return errors.Is(err, sentinel)
}

func logAgentV3SessionLoadError(tc *TurnContext, err error) {
	fields := []zap.Field{zap.String("agent", tc.Config.Name), zap.Int64("chat_id", tc.ChatID), zap.Error(err)}
	if isPureAgentV3SessionError(err, session.ErrMiss) {
		zap.L().Debug("agentv3: session miss; using legacy context", fields...)
	} else if !isPureAgentV3SessionError(err, errAgentV3SessionContextOverflow) && !isPureAgentV3SessionError(err, errAgentV3SessionMemoryEpoch) {
		zap.L().Warn("agentv3: session load rejected or failed; using legacy context", fields...)
	}
}

func setAgentV3SessionBaseline(tc *TurnContext, messages []*schema.Message, frameIndexes []int, currentStart int) {
	state := tc.Session
	if state == nil || state.capture == nil || state.baselineErr != nil {
		return
	}
	state.input = messages
	state.kinds = make([]agentV3SessionInputKind, len(messages))
	for i := range state.kinds {
		if i >= currentStart {
			state.kinds[i] = agentV3SessionCurrent
		} else {
			state.kinds[i] = agentV3SessionHistory
		}
	}
	for _, i := range frameIndexes {
		state.kinds[i] = agentV3SessionFrame
	}
}

func agentV3SessionArchive(state *agentV3SessionTurn, snapshot SessionCaptureResult) (session.TurnCapture, error) {
	if state.baselineErr != nil {
		return session.TurnCapture{}, state.baselineErr
	}
	if snapshot.Err != nil {
		return session.TurnCapture{}, snapshot.Err
	}
	if !snapshot.Complete || len(state.input) == 0 {
		return session.TurnCapture{}, errAgentV3SessionBaseline
	}
	// Match capture's SDK-specific normalization without erasing other Go runtime values.
	baseline, err := cloneSessionCaptureMessages(state.input)
	if err != nil {
		return session.TurnCapture{}, err
	}
	if !reflect.DeepEqual(baseline, snapshot.Input) {
		return session.TurnCapture{}, errAgentV3SessionBaseline
	}
	capture := session.TurnCapture{Complete: true}
	index := 0
	for _, message := range snapshot.ModelInput {
		if message.Role == schema.System {
			capture.Frame = append(capture.Frame, session.Record{Source: session.SourceFrame, Message: message})
			continue
		}
		for index < len(snapshot.Input) && snapshot.Input[index].Role == schema.System {
			index++
		}
		if index >= len(snapshot.Input) || !reflect.DeepEqual(snapshot.Input[index], message) {
			return session.TurnCapture{}, errAgentV3SessionModelMessagesChanged
		}
		switch state.kinds[index] {
		case agentV3SessionFrame:
			capture.Frame = append(capture.Frame, session.Record{Source: session.SourceFrame, Message: message})
		case agentV3SessionHistory:
			if state.parent == nil {
				capture.Bootstrap = append(capture.Bootstrap, session.History(message)...)
			}
		case agentV3SessionCurrent:
			capture.Delta = append(capture.Delta, session.History(message)...)
		}
		index++
	}
	for index < len(snapshot.Input) && snapshot.Input[index].Role == schema.System {
		index++
	}
	if index != len(snapshot.Input) {
		return session.TurnCapture{}, errAgentV3SessionModelMessagesOmitted
	}
	// Guidance stays inline as history so the next turn replays the exact model sequence.
	capture.Delta = append(capture.Delta, session.History(snapshot.Messages...)...)
	return capture, nil
}

// agentV3DeliveredMessages prefers the full chunk list and falls back to the single proven message.
func agentV3DeliveredMessages(all []*tb.Message, last *tb.Message) []*tb.Message {
	if len(all) > 0 {
		return all
	}
	if last != nil {
		return []*tb.Message{last}
	}
	return nil
}

// commitAgentV3Session publishes the turn under every delivered chunk ID so a reply to any
// chunk resolves to the node; the first ID is the primary message. It reports whether the
// node was published and records that on the turn state so the fallback raw-turn save knows
// whether the DAG actually holds this turn.
func commitAgentV3Session(tc *TurnContext, sent []*tb.Message) bool {
	ids := make([]int, 0, len(sent))
	for _, message := range sent {
		if message != nil && message.ID > 0 {
			ids = append(ids, message.ID)
		}
	}
	if tc == nil || tc.Background || tc.Session == nil || !tc.Session.save || tc.Session.capture == nil || len(ids) == 0 {
		return false
	}
	state := tc.Session
	snapshot := state.capture.Snapshot()
	capture, err := agentV3SessionArchive(state, snapshot)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), agentV3SessionCommitTimeout)
		defer cancel()
		var node session.Node
		node, err = state.service.Commit(ctx, session.CommitRequest{Scope: state.scope, Agent: tc.Config.Name, RunID: state.runID, Parent: state.parent, Capture: capture, Receipt: session.DeliveryReceipt{MessageIDs: ids}, MemoryEpoch: state.memoryEpoch})
		if err == nil {
			state.committed = true
			// The next replay of this node is its full model input plus this turn's new messages.
			scheduleAgentV3SessionCompaction(tc, node, append(slices.Clone(state.input), snapshot.Messages...))
			return true
		}
	}
	zap.L().Warn("agentv3: delivered response was not saved to session", zap.String("run_id", tc.RunID), zap.Ints("message_ids", ids), zap.Error(err))
	dropAgentV3SessionLatest(tc)
	return false
}

// dropAgentV3SessionLatest keeps an unpublished turn reachable after a latest hit: the turn
// only exists in the raw-turn fallback, so the next latest selection must miss instead of
// loading the same parent and skipping raw turns. Replies to the parent still resolve.
func dropAgentV3SessionLatest(tc *TurnContext) {
	state := tc.Session
	if state.parent == nil || state.selection.Mode != session.SelectLatest {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentV3SessionCommitTimeout)
	defer cancel()
	if err := state.service.DropLatest(ctx, state.scope, state.selection.Agent, state.parent.Ref()); err != nil {
		zap.L().Warn("agentv3: latest session index was not cleared after a failed commit", zap.String("run_id", tc.RunID), zap.Error(err))
	}
}
