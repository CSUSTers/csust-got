package agentv3

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"csust-got/config"
	"csust-got/cronjob"
	"csust-got/orm"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const cronLeaseGrace = 30 * time.Second
const cronReportTimeout = 30 * time.Second
const cronTraceFinishTimeout = 5 * time.Second

var (
	cronService                   atomic.Pointer[agentCronService]
	errCronBotIdentityUnavailable = errors.New("agentv3: cron requires initialized Telegram bot identity")
	errCronAlreadyStarted         = errors.New("agentv3: cron already started")
)

type cronRunnerConfigError struct {
	name   string
	reason string
}

func (e cronRunnerConfigError) Error() string {
	return fmt.Sprintf("agentv3: cron runner %q %s", e.name, e.reason)
}

type agentCronService struct {
	cfg                      config.AgentV3CronConfig
	store                    cronjob.Store
	bot                      *tb.Bot
	coordinator              cronjob.Coordinator
	staticWhite, staticBlock []int64
	policy                   func(context.Context, int64, int64) (orm.AgentCronPolicyState, error)
	now                      func() time.Time
	run                      func(context.Context, cronjob.Lease) agentCronRunResult
	report                   func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error)
	mu                       sync.Mutex
	cancel                   context.CancelFunc
	wg                       sync.WaitGroup
}

func validateCronStartup() error {
	var cfg *config.AgentV3Config
	if config.BotConfig != nil {
		cfg = config.BotConfig.AgentV3
	}
	if err := cfg.ValidateCron(); err != nil {
		return err
	}
	name := cfg.CronConfig().RunnerAgent
	if name == "" {
		return nil
	}
	if cfg == nil || !cfg.Enable || config.BotConfig.Agents == nil {
		return cronRunnerConfigError{name: name, reason: "is not enabled"}
	}
	count := 0
	var runner *config.AgentConfig
	for _, entry := range *config.BotConfig.Agents {
		if entry.Name == name {
			count++
			runner = entry
		}
	}
	if count != 1 || !runner.IsAgentV3Enabled() {
		return cronRunnerConfigError{name: name, reason: "must resolve to exactly one enabled agent"}
	}
	return nil
}

func initCronService() {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil || config.BotConfig.AgentV3.CronConfig().RunnerAgent == "" {
		return
	}
	cfg := config.BotConfig
	s := &agentCronService{cfg: cfg.AgentV3.CronConfig(), store: orm.NewAgentCronStore(), policy: orm.AgentCronPolicy, now: time.Now,
		staticWhite: slices.Clone(cfg.WhiteListConfig.Chats), staticBlock: slices.Clone(cfg.BlockListConfig.Chats)}
	s.run, s.report = s.generate, s.sendReport
	cronService.Store(s)
}

// StartCron starts the configured background agent scheduler and report workers.
func StartCron(ctx context.Context, bot *tb.Bot) error {
	s := cronService.Load()
	if s == nil {
		return nil
	}
	if bot == nil || bot.Me == nil || bot.Me.Username == "" {
		return errCronBotIdentityUnavailable
	}
	if _, err := s.runner(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errCronAlreadyStarted
	}
	s.bot = bot
	s.coordinator = cronjob.Coordinator{Bot: bot.Me.Username, Platform: agentV3Platform}
	ctx, s.cancel = context.WithCancel(ctx)
	// Separate loops and worker pools keep report/recovery polling alive during generation.
	s.wg.Add(2)
	go s.pollLoop(ctx, false)
	go s.pollLoop(ctx, true)
	return nil
}

func (s *agentCronService) stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *agentCronService) runner() (*CompiledAgent, error) {
	value, ok := compiledAgents.Load(s.cfg.RunnerAgent)
	if !ok {
		return nil, cronjob.ErrForbidden
	}
	cc := value.(*CompiledAgent)
	if cc.Config == nil || !cc.Config.IsAgentV3Enabled() || cc.Agent == nil {
		return nil, cronjob.ErrForbidden
	}
	return cc, nil
}

func (s *agentCronService) actor(tc *TurnContext) (cronjob.Actor, error) {
	if tc == nil || tc.Bot == nil || tc.Bot.Me == nil || tc.BotUser == nil || tc.BotUser.Username == "" || tc.BotUser.Username != tc.Bot.Me.Username || tc.Message == nil || tc.Message.Chat == nil || tc.Message.Sender == nil || tc.Message.Sender.ID == 0 || tc.ChatID == 0 || tc.ChatID != tc.Message.Chat.ID || tc.Config == nil {
		return cronjob.Actor{}, cronjob.ErrForbidden
	}
	scope := cronjob.Scope{Bot: tc.BotUser.Username, Platform: agentV3Platform, ChatID: tc.ChatID}
	if scope.Coordinator() != s.coordinator {
		return cronjob.Actor{}, cronjob.ErrForbidden
	}
	return cronjob.Actor{Scope: scope, UserID: tc.Message.Sender.ID}, nil
}

func (s *agentCronService) allowed(ctx context.Context, task cronjob.Task) error {
	if task.Scope.Coordinator() != s.coordinator || task.Scope.ChatID == 0 || task.CreatorID == 0 || config.BotConfig == nil {
		return cronjob.ErrForbidden
	}
	if config.BotConfig.Agents == nil || config.BotConfig.AgentV3 == nil || !config.BotConfig.AgentV3.Enable {
		return cronjob.ErrForbidden
	}
	var source, runner *config.AgentConfig
	for _, entry := range *config.BotConfig.Agents {
		if entry.Name == task.SourceAgent {
			source = entry
		}
		if entry.Name == s.cfg.RunnerAgent {
			runner = entry
		}
	}
	for _, entry := range []*config.AgentConfig{source, runner} {
		if entry == nil || !entry.IsAgentV3Enabled() || !HasCompiledAgent(entry.Name) {
			return cronjob.ErrForbidden
		}
		for _, filter := range entry.Filters.Filters {
			if filter.Type == filterTypeWhitelist && !slices.Contains(filter.Whitelist, task.Scope.ChatID) && !slices.Contains(filter.Whitelist, task.CreatorID) {
				return cronjob.ErrForbidden
			}
		}
	}
	state, err := s.policy(ctx, task.Scope.ChatID, task.CreatorID)
	if err != nil {
		return cronjob.ErrUnavailable
	}
	chat, user := strconv.FormatInt(task.Scope.ChatID, 10), strconv.FormatInt(task.CreatorID, 10)
	if state.Shutdown || state.Banned || slices.Contains(s.staticBlock, task.Scope.ChatID) || slices.Contains(s.staticBlock, task.CreatorID) || slices.Contains(state.BlockList, chat) || slices.Contains(state.BlockList, user) {
		return cronjob.ErrForbidden
	}
	if config.BotConfig.WhiteListConfig.Enabled && !slices.Contains(s.staticWhite, task.Scope.ChatID) && !slices.Contains(state.WhiteList, chat) {
		return cronjob.ErrForbidden
	}
	return nil
}

func (s *agentCronService) pollLoop(ctx context.Context, reports bool) {
	defer s.wg.Done()
	slots := make(chan struct{}, s.cfg.MaxConcurrency)
	ticker := time.NewTicker(time.Duration(s.cfg.PollIntervalMinutes) * time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if reports {
			s.pollReports(pollCtx, ctx, slots)
		} else {
			s.pollDue(pollCtx, ctx, slots)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *agentCronService) launch(ctx context.Context, slots chan struct{}, work func()) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case slots <- struct{}{}:
		s.wg.Add(1)
		go func() { defer s.wg.Done(); defer func() { <-slots }(); work() }()
		return true
	default:
		return false
	}
}

func (s *agentCronService) launchReserved(slots chan struct{}, work func()) {
	s.wg.Add(1)
	go func() { defer s.wg.Done(); defer func() { <-slots }(); work() }()
}

func (s *agentCronService) pollDue(ctx, lifetime context.Context, slots chan struct{}) {
	now := s.now()
	if _, err := s.store.Recover(ctx, cronjob.RecoverRequest{Coordinator: s.coordinator, Now: now, Limit: 100}); err != nil {
		cronLogFailure("recover")
		return
	}
	candidates, err := s.store.Due(ctx, cronjob.DueRequest{Coordinator: s.coordinator, Now: now, Limit: 100})
	if err != nil {
		cronLogFailure("due")
		return
	}
	for _, candidate := range candidates {
		select {
		case <-lifetime.Done():
			return
		case slots <- struct{}{}:
		default:
			return
		}
		lease, err := s.claimCandidate(ctx, candidate)
		if err != nil {
			<-slots
			continue
		}
		s.launchReserved(slots, func() { s.execute(lifetime, lease) })
		if len(slots) == cap(slots) {
			break
		}
	}
}

func (s *agentCronService) executeCandidate(ctx context.Context, candidate cronjob.Candidate) {
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lease, err := s.claimCandidate(claimCtx, candidate)
	if err != nil {
		return
	}
	s.execute(ctx, lease)
}

func (s *agentCronService) claimCandidate(ctx context.Context, candidate cronjob.Candidate) (cronjob.Lease, error) {
	timeout := s.cfg.RunTimeoutDuration()
	if runner, err := s.runner(); err == nil {
		timeout = min(timeout, runner.Config.GetTimeout())
	}
	return s.store.Claim(ctx, cronjob.ClaimRequest{Candidate: candidate, RunID: newAgentV3RunID(), Now: s.now(), RunTimeout: timeout, GracePeriod: cronLeaseGrace, MaxConcurrency: s.cfg.MaxConcurrency, ChatCooldown: time.Duration(s.cfg.ChatCooldownSeconds) * time.Second})
}

func (s *agentCronService) execute(ctx context.Context, lease cronjob.Lease) {
	runCtx, cancel := context.WithDeadline(ctx, lease.RunTimeoutAt)
	result := cronjob.ExecutionResult{RunID: lease.RunID, Kind: lease.Kind, TaskVersion: lease.TaskVersion, ScheduledAt: lease.ScheduledAt, StartedAt: s.now(), RetryOfRunID: lease.RetryOfRunID, RetryCount: lease.RetryCount, Format: "plain"}
	if err := s.allowed(runCtx, lease.Task); err != nil {
		result.Outcome, result.ErrorCode, result.ErrorMessage = cronjob.OutcomeSkipped, "policy_denied", "Current permissions do not allow this task."
		result.RetryStatus = cronjob.RetryUnavailable
		cancel()
		now := s.now()
		result.FinishedAt = now
		next, err := cronNext(lease.Task, now)
		if err != nil {
			cronLogFailure("next_run")
			return
		}
		req := cronjob.FinishRequest{Lease: lease, Result: result, Report: cronjob.Report{RunID: lease.RunID, TaskVersion: lease.TaskVersion, State: cronjob.DeliveryPending, NextAttemptAt: now}, NextRunAt: next, Now: now}
		s.persist(lease.ExpiresAt, func(ctx context.Context) error { _, err := s.store.Finish(ctx, req); return err })
		return
	}
	runResult := s.run(runCtx, lease)
	completionErr := runResult.Err
	if completionErr == nil {
		completionErr = runCtx.Err()
	}
	if completionErr != nil {
		result.Outcome, result.ErrorCode, result.ErrorMessage = cronjob.OutcomeFailed, "execution_failed", "Background execution failed or was interrupted."
		result.SideEffectsPossible = true
		result.RetryStatus = cronjob.RetryAvailable
	} else {
		result.Outcome, result.Text, result.RetryStatus = cronjob.OutcomeSucceeded, boundedCronText(runResult.Text), cronjob.RetryUnavailable
	}
	cancel()
	now := s.now()
	result.FinishedAt = now
	next, err := cronNext(lease.Task, now)
	if err != nil {
		cronLogFailure("next_run")
		s.finishCronTrace(ctx, runResult)
		return
	}
	req := cronjob.FinishRequest{Lease: lease, Result: result, Report: cronjob.Report{RunID: lease.RunID, TaskVersion: lease.TaskVersion, State: cronjob.DeliveryPending, NextAttemptAt: now}, NextRunAt: next, Now: now}
	s.persist(lease.ExpiresAt, func(ctx context.Context) error { _, err := s.store.Finish(ctx, req); return err })
	s.finishCronTrace(ctx, runResult)
}

func (s *agentCronService) finishCronTrace(ctx context.Context, result agentCronRunResult) {
	if result.Finalize == nil {
		return
	}
	finishCtx, cancel := context.WithTimeout(ctx, cronTraceFinishTimeout)
	defer cancel()
	result.Finalize(finishCtx)
}

func (s *agentCronService) pollReports(ctx, lifetime context.Context, slots chan struct{}) {
	candidates, err := s.store.Reports(ctx, cronjob.ReportsRequest{Coordinator: s.coordinator, Now: s.now(), Limit: 100})
	if err != nil {
		cronLogFailure("reports")
		return
	}
	for _, candidate := range candidates {
		if !s.launch(lifetime, slots, func() { s.deliverCandidate(lifetime, candidate) }) {
			break
		}
	}
}

func (s *agentCronService) deliverCandidate(ctx context.Context, candidate cronjob.ReportCandidate) {
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	lease, err := s.store.ClaimReport(claimCtx, cronjob.ClaimReportRequest{Candidate: candidate, Now: s.now(), Timeout: cronReportTimeout, GracePeriod: cronLeaseGrace})
	cancel()
	if err != nil {
		return
	}
	s.deliver(ctx, lease)
}

func (s *agentCronService) deliver(ctx context.Context, lease cronjob.ReportLease) {
	reportCtx, cancel := context.WithDeadline(ctx, lease.ExpiresAt.Add(-cronLeaseGrace))
	req := cronjob.ReportFinishRequest{Lease: lease}
	if err := s.allowed(reportCtx, lease.Task); err != nil {
		req.Suppressed = true
		req.ErrorMessage = "Report suppressed by current permissions or unavailable policy."
	} else {
		var err error
		req.Receipt, err = s.report(reportCtx, lease.Task)
		req.Delivered = err == nil
		if errors.Is(err, cronjob.ErrForbidden) || errors.Is(err, cronjob.ErrUnavailable) {
			req.Suppressed = true
		}
		if err != nil {
			req.ErrorMessage = "Telegram delivery failed; model execution will not be repeated."
		}
	}
	cancel()
	req.Now = s.now()
	req.NextAttemptAt = req.Now.Add(time.Minute)
	s.persist(lease.ExpiresAt, func(ctx context.Context) error { _, err := s.store.FinishReport(ctx, req); return err })
}

func (s *agentCronService) persist(deadline time.Time, save func(context.Context) error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for attempt := range 3 {
		callCtx, callCancel := context.WithTimeout(ctx, 3*time.Second)
		err := save(callCtx)
		callCancel()
		if err == nil || errors.Is(err, cronjob.ErrNotFound) || errors.Is(err, cronjob.ErrConflict) {
			return
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
	cronLogFailure("finish_persistence")
}

func cronLogFailure(operation string) {
	zap.L().Warn("agentv3: cron operation unavailable", zap.String("operation", operation))
}
