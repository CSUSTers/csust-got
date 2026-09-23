package agentv3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"csust-got/config"
	"csust-got/cronjob"

	tb "gopkg.in/telebot.v3"
)

var (
	errCronEmptyFinalResponse = errors.New("empty final response")
	errCronMissingResult      = errors.New("missing result")
)

type agentCronRunResult struct {
	Text     string
	Err      error
	Finalize func(context.Context)
}

func (s *agentCronService) generate(ctx context.Context, lease cronjob.Lease) (result agentCronRunResult) {
	compiled, err := s.runner()
	if err != nil {
		result.Err = err
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, compiled.Config.GetTimeout())
	defer cancel()
	task := lease.Task
	tc := &TurnContext{Bot: s.bot, BotUser: s.bot.Me, ChatID: task.Scope.ChatID, Config: compiled.Config, Background: true, RunID: lease.RunID,
		Message: &tb.Message{Text: task.Prompt, Unixtime: s.now().Unix(), ThreadID: int(task.ThreadID), Sender: &tb.User{ID: task.CreatorID}, Chat: &tb.Chat{ID: task.Scope.ChatID, Type: tb.ChatType(task.ChatType)}}}
	ctx = WithTurnContext(ctx, tc)
	messages, err := prepareAgentV3Turn(ctx, compiled, tc, &RichHistory{})
	if tc.V3 != nil && tc.V3.Trace != nil {
		trace, scope := tc.V3.Trace, tc.V3.Scope
		result.Finalize = func(finishCtx context.Context) { trace.FinishContext(finishCtx, scope) }
	}
	if err != nil {
		result.Err = err
		return result
	}
	response, err := compiled.Agent.Generate(ctx, messages)
	if err != nil {
		result.Err = err
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	if response == nil || strings.TrimSpace(response.Content) == "" {
		result.Err = errCronEmptyFinalResponse
		return result
	}
	result.Text = boundedCronText(response.Content)
	return result
}

func boundedCronText(text string) string {
	const limit = 12000
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "\n[truncated by bot]"
	}
	return text
}

func cronReportText(task cronjob.Task) string {
	r := task.LatestResult
	if r == nil {
		return ""
	}
	next := task.NextRunAt.Format(time.RFC3339)
	if task.NextRunAt.IsZero() {
		next = "无下一次计划（一次性已结束）"
	}
	header := fmt.Sprintf("Cron task %s\nRun %s — %s\nNext scheduled: %s\n\n", task.ID, r.RunID, r.Outcome, next)
	if r.Outcome == cronjob.OutcomeSucceeded {
		return header + r.Text
	}
	if r.Outcome == cronjob.OutcomeSkipped {
		return header + "Skipped: current permissions or policy availability did not permit execution. This occurrence will not be retried."
	}
	return header + "Execution failed or was interrupted. Earlier actions may already have caused side effects.\nTo explicitly request a retry, tell the bot: Retry cron task " + task.ID + " run " + r.RunID + ". The bot must get the current version before requesting retry; retries are limited."
}

type cronReportTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t cronReportTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req.WithContext(t.ctx))
}

func (s *agentCronService) sendReport(ctx context.Context, task cronjob.Task) (*cronjob.DeliveryReceipt, error) {
	receipt := &cronjob.DeliveryReceipt{}
	if task.Report != nil && task.Report.Receipt != nil {
		receipt.MessageIDs = slices.Clone(task.Report.Receipt.MessageIDs)
	}
	text := cronReportText(task)
	if text == "" {
		return receipt, errCronMissingResult
	}
	transport := http.DefaultTransport
	if config.BotConfig != nil && config.BotConfig.Proxy != "" {
		proxy, err := url.Parse(config.BotConfig.Proxy)
		if err != nil {
			return receipt, err
		}
		proxyTransport := &http.Transport{Proxy: http.ProxyURL(proxy)}
		defer proxyTransport.CloseIdleConnections()
		transport = proxyTransport
	}
	// Telebot's Send lacks a context argument. A private sender binds every HTTP
	// request to the report deadline without mutating the interactive bot client.
	bot, err := tb.NewBot(tb.Settings{Token: s.bot.Token, URL: s.bot.URL, Offline: true, Client: &http.Client{Timeout: cronReportTimeout, Transport: cronReportTransport{ctx: ctx, base: transport}}})
	if err != nil {
		return receipt, err
	}
	chunks := cronReportChunks(text)
	for i := len(receipt.MessageIDs); i < len(chunks); i++ {
		if err := ctx.Err(); err != nil {
			return receipt, err
		}
		// Recheck deletion and live permissions before every network send.
		current, err := s.store.Get(ctx, cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
		if err != nil {
			return receipt, err
		}
		if current.Report == nil || task.Report == nil || current.Report.RunID != task.Report.RunID || current.Report.TaskVersion != task.Report.TaskVersion || current.Report.LeaseToken != task.Report.LeaseToken {
			return receipt, cronjob.ErrConflict
		}
		if err := s.allowed(ctx, current); err != nil {
			return receipt, err
		}
		opts := &tb.SendOptions{ThreadID: int(task.ThreadID), AllowWithoutReply: true, DisableWebPagePreview: true, ParseMode: tb.ModeDefault}
		if task.SourceMessageID != 0 {
			opts.ReplyTo = &tb.Message{ID: task.SourceMessageID, Chat: &tb.Chat{ID: task.Scope.ChatID}}
		}
		message, err := bot.Send(&tb.Chat{ID: task.Scope.ChatID}, chunks[i], opts)
		if err != nil {
			return receipt, err
		}
		receipt.MessageIDs = append(receipt.MessageIDs, message.ID)
	}
	receipt.DeliveredAt = s.now()
	return receipt, nil
}

func cronReportChunks(text string) []string {
	// 1800 runes is <= 3600 UTF-16 units even for astral characters.
	runes := []rune(text)
	var chunks []string
	for len(runes) > 0 {
		n := min(1800, len(runes))
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}
