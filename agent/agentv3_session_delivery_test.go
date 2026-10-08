package agentv3

import (
	"context"
	"fmt"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestAgentV3SessionDeliveryHiddenThinkDoesNotPublishPlaceholder(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			d := newDeliveryTelegram(t)
			f.bot = d.bot
			useNative := false
			cfg := &config.AgentConfig{Name: "hidden", ContextMode: "reply_chain", Format: config.AgentOutputConfig{StreamOutput: streaming, EditInterval: "1h", UseNativeReasoning: &useNative, ProgressSummary: &config.ProgressSummaryConfig{Enable: true}}}
			f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("<think>hidden</think>", nil)}}})
			require.NoError(t, Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil))
			scopes, err := f.repo.Scopes(t.Context())
			require.NoError(t, err)
			require.Empty(t, scopes, "a placeholder is not a final delivery receipt")
			require.Empty(t, d.finalCalls(), "hidden-only final must not edit/send")
		})
	}
}

func TestAgentV3SessionDeliveryNativeReasoningOnlyIsArchived(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, reason := range []string{"quote", "collapse"} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, reason), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				d := newDeliveryTelegram(t)
				f.bot = d.bot
				cfg := &config.AgentConfig{Name: "reasoning", ContextMode: "reply_chain", Format: config.AgentOutputConfig{StreamOutput: streaming, Reason: reason, EditInterval: "1h"}}
				final := &schema.Message{Role: schema.Assistant, ReasoningContent: "NATIVE_REASONING_ONLY"}
				f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{final}}})
				require.NoError(t, Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil))
				calls := d.finalCalls()
				require.Len(t, calls, 1)
				require.Equal(t, FormatOutputWithReason("", final.ReasoningContent, &cfg.Format), calls[0].payload["text"])
				require.Positive(t, d.finalID)
				capture := f.archive(t, f.node(t, d.finalID))
				require.True(t, capture.Complete)
				require.Equal(t, final, capture.Delta[len(capture.Delta)-1].Message)
			})
		}
	}
}

func TestAgentV3SessionDeliveryFailedFinalDoesNotPublish(t *testing.T) {
	for _, mode := range []string{"send", "edit", "stream edit", "invalid edit receipt", "invalid send receipt", "invalid nonstream edit receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			d := newDeliveryTelegram(t)
			f.bot = d.bot
			cfg := &config.AgentConfig{Name: "failed-final", ContextMode: "reply_chain", Format: config.AgentOutputConfig{EditInterval: "1h"}}
			if mode != "send" && mode != "invalid send receipt" {
				cfg.Format.ProgressSummary = &config.ProgressSummaryConfig{Enable: true}
			}
			cfg.Format.StreamOutput = mode == "stream edit" || mode == "invalid edit receipt"
			d.invalidFinalID = mode == "invalid edit receipt" || mode == "invalid send receipt" || mode == "invalid nonstream edit receipt"
			d.failFinal = !d.invalidFinalID
			f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("final answer", nil)}}})
			err := Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil)
			if d.failFinal {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotEmpty(t, d.finalCalls(), "must exercise final delivery rather than placeholder failure")
			scopes, err := f.repo.Scopes(t.Context())
			require.NoError(t, err)
			require.Empty(t, scopes)
		})
	}
}

func TestAgentV3SessionDeliveryFallbackAndNotModifiedPublish(t *testing.T) {
	tests := []struct {
		name          string
		streaming     bool
		placeholder   bool
		failFormatted bool
		editError     string
	}{
		{name: "send fallback", failFormatted: true},
		{name: "edit fallback", placeholder: true, failFormatted: true},
		{name: "stream edit fallback", streaming: true, failFormatted: true},
		{name: "edit not modified", placeholder: true, editError: tb.ErrMessageNotModified.Description},
		{name: "stream not modified", streaming: true, editError: tb.ErrSameMessageContent.Description},
		{name: "edit fallback not modified", placeholder: true, failFormatted: true, editError: tb.ErrSameMessageContent.Description},
		{name: "stream fallback not modified", streaming: true, failFormatted: true, editError: tb.ErrMessageNotModified.Description},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			d := newDeliveryTelegram(t)
			f.bot = d.bot
			d.failFormatted, d.editError = tt.failFormatted, tt.editError
			cfg := &config.AgentConfig{Name: "fallback-delivery", ContextMode: "reply_chain", Format: config.AgentOutputConfig{StreamOutput: tt.streaming, EditInterval: "1h"}}
			if tt.placeholder {
				cfg.Format.ProgressSummary = &config.ProgressSummaryConfig{Enable: true}
			}
			final := schema.AssistantMessage("final answer", nil)
			f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{final}}})
			require.NoError(t, Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil))
			calls := d.finalCalls()
			wantCalls := 1
			if tt.failFormatted {
				wantCalls++
			}
			require.Len(t, calls, wantCalls, "not-modified must confirm delivery without an extra fallback")
			require.Equal(t, "final answer", calls[len(calls)-1].payload["text"])
			capture := f.archive(t, f.node(t, d.finalID))
			require.Equal(t, final, capture.Delta[len(capture.Delta)-1].Message)
		})
	}
}

func TestAgentV3SessionDeliveryRichRequiresSuccessfulFinalReceipt(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, outcome := range []string{"success", "send failure", "invalid receipt"} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, outcome), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				config.BotConfig.AgentV3.Enable = true
				d := newDeliveryTelegram(t)
				f.bot = d.bot
				d.failFinal, d.invalidFinalID = outcome == "send failure", outcome == "invalid receipt"
				cfg := &config.AgentConfig{Name: "rich-delivery", ContextMode: "reply_chain", Agent: &config.AgentOptions{Enable: true, Rich: true}, Format: config.AgentOutputConfig{StreamOutput: streaming, EditInterval: "1h", ProgressSummary: &config.ProgressSummaryConfig{Enable: true}}}
				final := schema.AssistantMessage(mustTelegramRichEnvelope("**rich final**"), nil)
				mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{final}}}, before: func(ctx context.Context, _ []*schema.Message) error {
					GetTurnContext(ctx).markSkillLoaded("rich-message")
					return nil
				}}
				f.compile(t, cfg, mdl)
				err := Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil)
				if d.failFinal {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				calls := d.finalCalls()
				require.Len(t, calls, 1)
				require.Equal(t, telegramSendRichMessageMethod, calls[0].method)
				if outcome == "success" {
					capture := f.archive(t, f.node(t, d.finalID))
					require.Equal(t, final, capture.Delta[len(capture.Delta)-1].Message)
				} else {
					scopes, err := f.repo.Scopes(t.Context())
					require.NoError(t, err)
					require.Empty(t, scopes, "failed rich send must not reuse the progress placeholder's ID")
				}
			})
		}
	}
}

func TestAgentV3SessionDeliveryPlaceholderFailureClosesCapture(t *testing.T) {
	f := newAgentSessionFixture(t)
	d := newDeliveryTelegram(t)
	f.bot = d.bot
	d.failPlaceholder = true
	cfg := &config.AgentConfig{Name: "early-placeholder-failure", ContextMode: "reply_chain", Format: config.AgentOutputConfig{StreamOutput: true}}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("blocked final", nil)}}})
	type streamState struct {
		reader  *schema.StreamReader[*schema.Message]
		capture *SessionCapture
		cancel  context.CancelFunc
	}
	states := make(chan streamState, 1)
	oldStream := streamAgentV3
	streamAgentV3 = func(agent *CustomAgent, ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		ctx, cancel := context.WithCancel(ctx)
		reader, err := oldStream(agent, ctx, input)
		states <- streamState{reader: reader, capture: sessionCaptureFromContext(ctx), cancel: cancel}
		return reader, err
	}
	t.Cleanup(func() { streamAgentV3 = oldStream })
	returned := make(chan error, 1)
	go func() {
		returned <- Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil)
	}()
	var state streamState
	select {
	case state = <-states:
	case <-time.After(2 * time.Second):
		t.Fatal("Chat did not obtain the output reader")
	}
	t.Cleanup(func() {
		state.cancel()
		select {
		case <-state.capture.Done():
			return
		default:
			state.reader.Close()
		}
		select {
		case <-state.capture.Done():
		case <-time.After(2 * time.Second):
			t.Error("capture did not stop after test cleanup")
		}
	})
	require.NotNil(t, state.capture)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Chat did not return after placeholder failure")
	}
	select {
	case <-state.capture.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Chat returned but capture writer is still blocked on the unbuffered output pipe")
	}
	require.False(t, state.capture.Snapshot().Complete)
	scopes, err := f.repo.Scopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, scopes)
}
