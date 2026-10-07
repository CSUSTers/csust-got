package agentv3

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
)

type agentV3SessionService struct {
	service *session.Service
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
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
}

func initAgentV3SessionService(ctx context.Context) error {
	if config.BotConfig == nil {
		return nil
	}
	cfg := config.AgentV3SessionConfig{}
	if config.BotConfig.AgentV3 != nil {
		cfg = config.BotConfig.AgentV3.Session
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("agentv3: session configuration: %w", err)
	}
	repo, err := orm.NewProductionAgentV3SessionRepository()
	if err != nil {
		return fmt.Errorf("agentv3: session repository: %w", err)
	}
	files, err := session.NewFileStore(cfg.DirectoryPath())
	if err != nil {
		return fmt.Errorf("agentv3: session directory: %w", err)
	}
	service, err := session.NewService(repo, files, session.Options{TTL: cfg.IdleTTL()})
	if err != nil {
		_ = files.Close()
		return fmt.Errorf("agentv3: session service: %w", err)
	}
	s := startAgentV3SessionMaintenance(ctx, service, time.Local)
	if old := agentSessionService.Swap(s); old != nil {
		old.close()
	}
	return nil
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
					s.collect(ctx)
					nextCollection, err = session.NextCollection(time.Now(), location)
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

func (s *agentV3SessionService) collect(ctx context.Context) {
	err := s.service.Collect(ctx)
	if err != nil && ctx.Err() == nil {
		zap.L().Warn("agentv3: session collection failed; pending/deleting recovery will retry", zap.Error(err))
	}
}

func (s *agentV3SessionService) close() {
	s.once.Do(func() {
		s.cancel()
		if err := s.service.Close(); err != nil {
			zap.L().Warn("agentv3: session close failed", zap.Error(err))
		}
		<-s.done
	})
}

func loadAgentV3Session(ctx context.Context, tc *TurnContext) {
	if tc.Background {
		return
	}
	state := &agentV3SessionTurn{scope: session.Scope{Bot: agentV3BotName(tc), Platform: agentV3Platform, ChatID: tc.ChatID}}
	tc.Session = state
	s := agentSessionService.Load()
	if s == nil {
		return
	}
	save, load := tc.Config.EffectiveSessionSettings(tc.Trigger)
	state.service = s.service
	if save {
		state.runID, state.baselineErr = session.NewID()
		state.capture = NewSessionCapture()
	}
	if !load {
		return
	}
	selection := session.Selection{Scope: state.scope, Agent: tc.Config.Name, Mode: session.SelectLatest}
	if tc.Trigger != nil && tc.Trigger.Reply {
		selection.Mode = session.SelectReply
		if tc.Message.ReplyTo != nil {
			selection.ReplyMessageID = tc.Message.ReplyTo.ID
		}
	}
	loaded, err := state.service.Load(ctx, selection)
	if err != nil {
		zap.L().Warn("agentv3: session load failed; using legacy context", zap.String("agent", tc.Config.Name), zap.Int64("chat_id", tc.ChatID), zap.Error(err))
		return
	}
	state.parent, state.replay = loaded.Parent, loaded.Messages
}

func closeAgentV3SessionTurn(tc *TurnContext) {
	if tc.Session != nil && tc.Session.parent != nil {
		if err := tc.Session.parent.Close(); err != nil {
			zap.L().Warn("agentv3: session parent release failed", zap.Error(err))
		}
	}
}

func buildAgentV3SessionInput(cc *CompiledAgent, tc *TurnContext) ([]*schema.Message, error) {
	if !tc.Config.UsesReplyChain() {
		current := *tc.Message
		current.ReplyTo = nil
		currentTC := &TurnContext{Bot: tc.Bot, BotUser: tc.BotUser, Message: &current, ChatID: tc.ChatID, Config: tc.Config, Trigger: tc.Trigger, V3: tc.V3}
		message, err := buildAgentV3UserMessage(cc, currentTC, &RichHistory{}, nil)
		if err != nil {
			return nil, err
		}
		return []*schema.Message{message}, nil
	}
	current, err := buildReplySessionMessages(cc, tc, replySession{Blocks: []replySessionBlock{{Messages: []*tb.Message{tc.Message}, Current: true}}}, 0)
	if err != nil {
		return nil, err
	}
	addition, err := buildReplySessionPromptAddition(cc, tc)
	if err != nil {
		return nil, err
	}
	return append([]*schema.Message{addition}, current...), nil
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
	for _, guidance := range snapshot.Guidance {
		capture.Frame = append(capture.Frame, session.Record{Source: session.SourceGuidance, Message: guidance.Message})
	}
	capture.Delta = append(capture.Delta, session.History(snapshot.Messages...)...)
	return capture, nil
}

func commitAgentV3Session(tc *TurnContext, sent *tb.Message) {
	if tc == nil || tc.Background || tc.Session == nil || tc.Session.capture == nil || sent == nil || sent.ID <= 0 {
		return
	}
	state := tc.Session
	capture, err := agentV3SessionArchive(state, state.capture.Snapshot())
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), agentV3SessionCommitTimeout)
		defer cancel()
		_, err = state.service.Commit(ctx, session.CommitRequest{Scope: state.scope, Agent: tc.Config.Name, RunID: state.runID, Parent: state.parent, Capture: capture, Receipt: session.DeliveryReceipt{MessageIDs: []int{sent.ID}}})
	}
	if err != nil {
		zap.L().Warn("agentv3: delivered response was not saved to session", zap.String("run_id", tc.RunID), zap.Int("message_id", sent.ID), zap.Error(err))
	}
}
