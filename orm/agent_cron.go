package orm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
)

const (
	agentCronWatchAttempts      = 64
	agentCronReportLifetime     = 24 * time.Hour
	agentCronAutoReportAttempts = 3
	agentCronMaxPromptBytes     = 64 * 1024
	agentCronMaxResultBytes     = 64 * 1024
	agentCronMaxErrorBytes      = 2048
	agentCronMaxAttachments     = 32
	agentCronMaxReceiptIDs      = 256
	agentCronNegativeInfinity   = "-inf"
	agentCronReportWindowError  = "report delivery window expired"
)

var (
	errInvalidAgentCronTaskMember = errors.New("invalid cron task member")
	errInvalidAgentCronChatLock   = errors.New("invalid cron chat lock")
)

type agentCronStore struct {
	client *redis.Client
}

type storedAgentCronTask struct {
	Task                cronjob.Task `json:"task"`
	DedupKey            string       `json:"dedup_key"`
	RetryReadyAt        time.Time    `json:"retry_ready_at,omitempty"`
	ManualReportRetries int          `json:"manual_report_retries,omitempty"`
}

type agentCronExecutionLease struct {
	Token       string        `json:"token"`
	Scope       cronjob.Scope `json:"scope"`
	TaskID      string        `json:"task_id"`
	TaskVersion int64         `json:"task_version"`
	RunID       string        `json:"run_id"`
	ExpiresAt   time.Time     `json:"expires_at"`
}

type agentCronReportLease struct {
	Token       string        `json:"token"`
	Scope       cronjob.Scope `json:"scope"`
	TaskID      string        `json:"task_id"`
	TaskVersion int64         `json:"task_version"`
	RunID       string        `json:"run_id"`
	Attempt     int           `json:"attempt"`
	ExpiresAt   time.Time     `json:"expires_at"`
}

// NewAgentCronStore returns the Redis-backed cron store using the package client.
func NewAgentCronStore() cronjob.Store {
	return &agentCronStore{client: rc}
}

func (s *agentCronStore) watchCoordinator(ctx context.Context, coordinator cronjob.Coordinator, fn func(*redis.Tx, redis.Pipeliner) error) error {
	if err := validateCoordinator(coordinator); err != nil {
		return err
	}
	guard := s.guardKey(coordinator)
	for range agentCronWatchAttempts {
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			var operationErr error
			_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				operationErr = fn(tx, pipe)
				if operationErr != nil {
					return operationErr
				}
				pipe.Incr(ctx, guard)
				return nil
			})
			if operationErr != nil {
				_, verifyErr := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.Exists(ctx, guard)
					return nil
				})
				if verifyErr != nil {
					return verifyErr
				}
				return operationErr
			}
			return err
		}, guard)
		if !errors.Is(err, redis.TxFailedErr) {
			return agentCronPublicError(err)
		}
	}
	return cronjob.NewError(cronjob.CodeConflict, "cron state changed concurrently")
}

func agentCronPublicError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *cronjob.Error
	if errors.As(err, &domainErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return cronjob.NewError(cronjob.CodeUnavailable, "cron storage is unavailable")
}

func validateCoordinator(coordinator cronjob.Coordinator) error {
	if strings.TrimSpace(coordinator.Bot) == "" || strings.TrimSpace(coordinator.Platform) == "" {
		return cronjob.NewError(cronjob.CodeInvalidArgument, "bot and platform are required")
	}
	return nil
}

func validateScope(scope cronjob.Scope) error {
	if err := validateCoordinator(scope.Coordinator()); err != nil {
		return err
	}
	if scope.ChatID == 0 {
		return cronjob.NewError(cronjob.CodeInvalidArgument, "chat id is required")
	}
	return nil
}

func normalizeCronValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizePromptValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func validateTaskText(cron, timezone, prompt string) error {
	if cron == "" || strings.TrimSpace(timezone) == "" || prompt == "" {
		return cronjob.NewError(cronjob.CodeInvalidArgument, "cron, timezone, and prompt are required")
	}
	if !utf8.ValidString(prompt) || len(prompt) > agentCronMaxPromptBytes {
		return cronjob.NewError(cronjob.CodeInvalidArgument, "prompt is invalid or too large")
	}
	return nil
}

func agentCronDedupKey(creatorID int64, cron, timezone, prompt string) string {
	payload := fmt.Sprintf("%d\x00%s\x00%s\x00%s", creatorID, cron, timezone, prompt)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func agentCronRandomID(prefix string) (string, error) {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data), nil
}

func agentCronEncode(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (s *agentCronStore) baseKey(coordinator cronjob.Coordinator) string {
	return wrapKey("cron:v1:" + agentCronEncode(coordinator.Bot) + ":" + agentCronEncode(coordinator.Platform))
}

func (s *agentCronStore) guardKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":guard"
}

func (s *agentCronStore) taskKey(scope cronjob.Scope, taskID string) string {
	return fmt.Sprintf("%s:c%d:task:%s", s.baseKey(scope.Coordinator()), scope.ChatID, agentCronEncode(taskID))
}

func (s *agentCronStore) tasksKey(scope cronjob.Scope) string {
	return fmt.Sprintf("%s:c%d:tasks", s.baseKey(scope.Coordinator()), scope.ChatID)
}

func (s *agentCronStore) dedupKey(scope cronjob.Scope) string {
	return fmt.Sprintf("%s:c%d:dedup", s.baseKey(scope.Coordinator()), scope.ChatID)
}

func (s *agentCronStore) dueKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":due"
}

func (s *agentCronStore) retryKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":retry"
}

func (s *agentCronStore) reportsKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":reports"
}

func (s *agentCronStore) leasesKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":leases"
}

func (s *agentCronStore) leaseKey(coordinator cronjob.Coordinator, token string) string {
	return s.baseKey(coordinator) + ":lease:" + agentCronEncode(token)
}

func (s *agentCronStore) reportLeasesKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":report_leases"
}

func (s *agentCronStore) reportLeaseKey(coordinator cronjob.Coordinator, token string) string {
	return s.baseKey(coordinator) + ":report_lease:" + agentCronEncode(token)
}

func (s *agentCronStore) slotsKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":slots"
}

func (s *agentCronStore) chatLocksKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":chat_locks"
}

func (s *agentCronStore) cooldownsKey(coordinator cronjob.Coordinator) string {
	return s.baseKey(coordinator) + ":cooldowns"
}

func agentCronTaskMember(scope cronjob.Scope, taskID string) string {
	return strconv.FormatInt(scope.ChatID, 10) + "|" + agentCronEncode(taskID)
}

func decodeAgentCronTaskMember(member string, coordinator cronjob.Coordinator) (cronjob.Scope, string, error) {
	parts := strings.SplitN(member, "|", 2)
	if len(parts) != 2 {
		return cronjob.Scope{}, "", errInvalidAgentCronTaskMember
	}
	chatID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return cronjob.Scope{}, "", err
	}
	taskID, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cronjob.Scope{}, "", err
	}
	return cronjob.Scope{Bot: coordinator.Bot, Platform: coordinator.Platform, ChatID: chatID}, string(taskID), nil
}

func loadAgentCronTask(ctx context.Context, cmd redis.Cmdable, key string) (*storedAgentCronTask, error) {
	data, err := cmd.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored storedAgentCronTask
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

func marshalAgentCron(value any) ([]byte, error) {
	return json.Marshal(value)
}

func agentCronTimeScore(value time.Time) float64 {
	return float64(value.UnixMilli())
}

func agentCronReportTerminal(state cronjob.DeliveryState) bool {
	switch state {
	case cronjob.DeliveryDelivered, cronjob.DeliveryFailed, cronjob.DeliveryExhausted, cronjob.DeliverySuppressed:
		return true
	default:
		return false
	}
}

func agentCronReportBlocks(task cronjob.Task) bool {
	return task.Report != nil && !agentCronReportTerminal(task.Report.State)
}

func agentCronReportExpired(task cronjob.Task, now time.Time) bool {
	return task.LatestResult != nil && !task.LatestResult.FinishedAt.IsZero() && !now.Before(task.LatestResult.FinishedAt.Add(agentCronReportLifetime))
}

func agentCronTruncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func agentCronBoundResult(result cronjob.ExecutionResult) cronjob.ExecutionResult {
	result.Text = agentCronTruncate(result.Text, agentCronMaxResultBytes)
	result.ErrorCode = agentCronTruncate(result.ErrorCode, 128)
	result.ErrorMessage = agentCronTruncate(result.ErrorMessage, agentCronMaxErrorBytes)
	if len(result.Attachments) > agentCronMaxAttachments {
		result.Attachments = result.Attachments[:agentCronMaxAttachments]
	}
	return result
}

func agentCronMergeReceipt(current, update *cronjob.DeliveryReceipt) *cronjob.DeliveryReceipt {
	if current == nil && update == nil {
		return nil
	}
	merged := &cronjob.DeliveryReceipt{}
	seen := make(map[int]struct{})
	for _, receipt := range []*cronjob.DeliveryReceipt{current, update} {
		if receipt == nil {
			continue
		}
		if receipt.DeliveredAt.After(merged.DeliveredAt) {
			merged.DeliveredAt = receipt.DeliveredAt
		}
		for _, id := range receipt.MessageIDs {
			if len(merged.MessageIDs) >= agentCronMaxReceiptIDs {
				break
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				merged.MessageIDs = append(merged.MessageIDs, id)
			}
		}
	}
	return merged
}
