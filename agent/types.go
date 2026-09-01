//go:build !386 && !arm

package agentv3

import (
	"context"
	"csust-got/config"
	model "github.com/cloudwego/eino/components/model"
	tb "gopkg.in/telebot.v3"
	"sync"
	"sync/atomic"
	"text/template"
	"time"
)

// turnContextKey is the Go context key for per-request runtime data.
type turnContextKey struct{}

// TurnContext holds per-request runtime data passed through Go context.
// Tools and subagents access this to interact with Telegram, Redis, etc.
type TurnContext struct {
	Bot           *tb.Bot
	Message       *tb.Message
	ChatID        int64
	Config        *config.AgentConfig
	Trigger       *config.AgentTrigger
	BotUser       *tb.User
	RunID         string
	Namespace     string
	RuntimeClient *RemoteRuntimeClient
	V3            *AgentV3TurnState
	// Progress tracking — used by update_progress tool and streaming handlers.
	// editMu serializes ALL edits to progressMsg to avoid Telegram race conditions.
	editMu           sync.Mutex
	progressMsg      *tb.Message                // Placeholder message for progress/streaming
	progressSteps    []progressStep             // Structured step/detail progress shown by update_progress
	progressModel    model.ToolCallingChatModel // Lazily-built small model for summarization
	progressOnce     sync.Once                  // Ensures progressModel is built once
	progressModelErr error                      // Error from building progressModel
	streamingStarted atomic.Bool                // Set true when streaming/final output begins
	finalized        atomic.Bool                // Set true after final response sent
	lastEditAt       atomic.Int64               // Unix nanoseconds of the last Telegram edit; shared rate-limit floor.
	toolMu           sync.Mutex
}

type progressStep struct {
	Title     string
	Details   []string
	Completed bool
}

// ShouldAllowEdit returns true if at least min has elapsed since the last edit.
// Pass a zero or negative duration to always allow.
func (tc *TurnContext) ShouldAllowEdit(min time.Duration) bool {
	if tc == nil || min <= 0 {
		return true
	}
	last := tc.lastEditAt.Load()
	if last == 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) >= min
}

// MarkEdited records now as the most recent edit time.
func (tc *TurnContext) MarkEdited() {
	if tc == nil {
		return
	}
	tc.lastEditAt.Store(time.Now().UnixNano())
}

func (tc *TurnContext) richMessageSkillLoadedForFinal() bool {
	if tc == nil || tc.Config == nil || !tc.Config.IsAgentV3RichEnabled() {
		return false
	}
	return tc.hasLoadedSkill("rich-message")
}

func (tc *TurnContext) markSkillLoaded(name string) {
	if tc == nil {
		return
	}
	canonical, err := parseAgentV3CanonicalSkillName(name)
	if err != nil {
		return
	}
	tc.toolMu.Lock()
	defer tc.toolMu.Unlock()
	if tc.V3 == nil {
		return
	}
	if tc.V3.loadedSkillNames == nil {
		tc.V3.loadedSkillNames = make(map[string]struct{})
	}
	tc.V3.loadedSkillNames[canonical] = struct{}{}
}

func (tc *TurnContext) hasLoadedSkill(name string) bool {
	if tc == nil {
		return false
	}
	canonical, err := parseAgentV3CanonicalSkillName(name)
	if err != nil {
		return false
	}
	tc.toolMu.Lock()
	defer tc.toolMu.Unlock()
	if tc.V3 == nil {
		return false
	}
	_, ok := tc.V3.loadedSkillNames[canonical]
	return ok
}

// WithTurnContext stores TurnContext in a Go context.
func WithTurnContext(ctx context.Context, tc *TurnContext) context.Context {
	return context.WithValue(ctx, turnContextKey{}, tc)
}

// GetTurnContext retrieves TurnContext from a Go context.
func GetTurnContext(ctx context.Context) *TurnContext {
	if v, ok := ctx.Value(turnContextKey{}).(*TurnContext); ok {
		return v
	}
	return nil
}

// SetProgressMsg atomically sets the Telegram placeholder message for progress updates.
func (tc *TurnContext) SetProgressMsg(msg *tb.Message) {
	tc.editMu.Lock()
	defer tc.editMu.Unlock()
	tc.progressMsg = msg
}

// GetProgressMsg atomically retrieves the current progress placeholder message.
func (tc *TurnContext) GetProgressMsg() *tb.Message {
	tc.editMu.Lock()
	defer tc.editMu.Unlock()
	return tc.progressMsg
}

// GetOrBuildProgressModel lazily builds and returns the progress summarization model.
// Returns (nil, nil) if no progress summary model is configured.
func (tc *TurnContext) GetOrBuildProgressModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	psCfg := tc.Config.Format.ProgressSummary
	if psCfg == nil || psCfg.Model == nil {
		return nil, nil
	}
	tc.progressOnce.Do(func() {
		tc.progressModel, tc.progressModelErr = buildModel(ctx, psCfg.Model)
	})
	return tc.progressModel, tc.progressModelErr
}

// CompiledAgent is a pre-compiled agent configuration ready for concurrent reuse.
// Created once at init time, used for every incoming request matching this agent configuration.
type CompiledAgent struct {
	Name                 string
	Config               *config.AgentConfig
	Agent                *CustomAgent
	SystemTemplate       *template.Template
	PromptTemplate       *template.Template
	SkillPromptAddons    string
	AgentV3StartupSkills *agentV3StartupSkillSnapshots
	AgentV3SkillSources  []agentV3SkillSnapshot
	AgentV3SkillCatalog  agentV3SkillCatalog
}

// RichHistory keeps both the rendered text context and the underlying Telegram
// messages so agent v3 can recover media attachments for multimodal input.
type RichHistory struct {
	ContextMessages []*ContextMessage
	FullMessages    []*tb.Message
}

// PromptData is the template rendering data exposed to agent v3 prompt templates.
type PromptData struct {
	DateTime        string
	CurrentDateCN   string
	Input           string
	ContextMessages []*ContextMessage
	ContextText     string
	ContextXml      string
	ReplyToXml      string
	BotUsername     string
}
