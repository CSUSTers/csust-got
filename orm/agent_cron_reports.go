package orm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
)

func (s *agentCronStore) Reports(ctx context.Context, request cronjob.ReportsRequest) ([]cronjob.ReportCandidate, error) {
	if err := validateCoordinator(request.Coordinator); err != nil {
		return nil, err
	}
	if request.Now.IsZero() {
		return nil, cronjob.NewError(cronjob.CodeInvalidArgument, "current time is required")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if err := s.recoverExpiredReportLeases(ctx, request.Coordinator, request.Now, limit); err != nil {
		return nil, err
	}
	members, err := s.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key: s.reportsKey(request.Coordinator), Start: agentCronNegativeInfinity,
		Stop: strconv.FormatInt(request.Now.UnixMilli(), 10), ByScore: true,
	}).Result()
	if err != nil {
		return nil, agentCronPublicError(err)
	}
	candidates := make([]cronjob.ReportCandidate, 0, limit)
	for _, member := range members {
		if len(candidates) == limit {
			break
		}
		scope, taskID, decodeErr := decodeAgentCronTaskMember(member, request.Coordinator)
		if decodeErr != nil {
			continue
		}
		stored, loadErr := loadAgentCronTask(ctx, s.client, s.taskKey(scope, taskID))
		if loadErr != nil {
			return nil, agentCronPublicError(loadErr)
		}
		if stored == nil || stored.Task.Report == nil || stored.Task.Report.State != cronjob.DeliveryPending || stored.Task.Report.NextAttemptAt.After(request.Now) {
			continue
		}
		if agentCronReportExpired(stored.Task, request.Now) {
			if expireErr := s.expirePendingReport(ctx, scope, taskID, request.Now); expireErr != nil {
				return nil, expireErr
			}
			continue
		}
		candidates = append(candidates, cronjob.ReportCandidate{
			Scope: scope, TaskID: taskID, TaskVersion: stored.Task.Version,
			RunID: stored.Task.Report.RunID, Attempt: stored.Task.Report.Attempt, DueAt: stored.Task.Report.NextAttemptAt,
		})
	}
	return candidates, nil
}

func (s *agentCronStore) ClaimReport(ctx context.Context, request cronjob.ClaimReportRequest) (cronjob.ReportLease, error) {
	var lease cronjob.ReportLease
	if err := validateScope(request.Candidate.Scope); err != nil {
		return lease, err
	}
	if request.Candidate.TaskID == "" || request.Candidate.TaskVersion <= 0 || request.Candidate.RunID == "" || request.Now.IsZero() || request.Timeout <= 0 || request.GracePeriod < 0 {
		return lease, cronjob.NewError(cronjob.CodeInvalidArgument, "invalid report claim request")
	}
	scope := request.Candidate.Scope
	coordinator := scope.Coordinator()
	var postErr error
	err := s.watchCoordinator(ctx, coordinator, func(tx *redis.Tx, pipe redis.Pipeliner) error {
		lease = cronjob.ReportLease{}
		postErr = nil
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.Candidate.TaskID))
		if err != nil {
			return err
		}
		if stored == nil || stored.Task.Scope != scope {
			return cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
		}
		if stored.Task.Version != request.Candidate.TaskVersion || stored.Task.Report == nil || stored.Task.Report.RunID != request.Candidate.RunID || stored.Task.Report.TaskVersion != request.Candidate.TaskVersion || stored.Task.Report.State != cronjob.DeliveryPending || stored.Task.Report.Attempt != request.Candidate.Attempt || !stored.Task.Report.NextAttemptAt.Equal(request.Candidate.DueAt) || stored.Task.Report.NextAttemptAt.After(request.Now) {
			return cronjob.NewError(cronjob.CodeConflict, "report candidate is stale")
		}
		member := agentCronTaskMember(scope, stored.Task.ID)
		score, scoreErr := tx.ZScore(ctx, s.reportsKey(coordinator), member).Result()
		if scoreErr != nil && !errors.Is(scoreErr, redis.Nil) {
			return scoreErr
		}
		if errors.Is(scoreErr, redis.Nil) || int64(score) != stored.Task.Report.NextAttemptAt.UnixMilli() {
			return cronjob.NewError(cronjob.CodeConflict, "report candidate is stale")
		}
		if agentCronReportExpired(stored.Task, request.Now) {
			stored.Task.Report.State = cronjob.DeliveryFailed
			stored.Task.Report.LastError = agentCronReportWindowError
			stored.Task.Report.NextAttemptAt = time.Time{}
			stored.Task.UpdatedAt = request.Now
			data, marshalErr := marshalAgentCron(stored)
			if marshalErr != nil {
				return marshalErr
			}
			pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
			postErr = cronjob.NewError(cronjob.CodeConflict, agentCronReportWindowError)
			return nil
		}
		allowed := agentCronAutoReportAttempts + stored.ManualReportRetries
		if stored.Task.Report.Attempt >= allowed {
			stored.Task.Report.State = cronjob.DeliveryExhausted
			stored.Task.Report.NextAttemptAt = time.Time{}
			stored.Task.UpdatedAt = request.Now
			data, marshalErr := marshalAgentCron(stored)
			if marshalErr != nil {
				return marshalErr
			}
			pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
			postErr = cronjob.NewError(cronjob.CodeConflict, "report retry budget exhausted")
			return nil
		}
		token, err := agentCronRandomID("report_")
		if err != nil {
			return err
		}
		expiresAt := request.Now.Add(request.Timeout).Add(request.GracePeriod)
		stored.Task.Report.Attempt++
		stored.Task.Report.State = cronjob.DeliveryClaimed
		stored.Task.Report.LeaseToken = token
		stored.Task.Report.LeaseExpiresAt = expiresAt
		stored.Task.UpdatedAt = request.Now
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		record := agentCronReportLease{Token: token, Scope: scope, TaskID: stored.Task.ID, TaskVersion: stored.Task.Version, RunID: stored.Task.Report.RunID, Attempt: stored.Task.Report.Attempt, ExpiresAt: expiresAt}
		recordData, err := marshalAgentCron(record)
		if err != nil {
			return err
		}
		pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
		pipe.ZRem(ctx, s.reportsKey(coordinator), member)
		pipe.Set(ctx, s.reportLeaseKey(coordinator, token), recordData, 0)
		pipe.ZAdd(ctx, s.reportLeasesKey(coordinator), redis.Z{Score: agentCronTimeScore(expiresAt), Member: token})
		lease = cronjob.ReportLease{Task: stored.Task, RunID: record.RunID, Token: token, Attempt: record.Attempt, ExpiresAt: expiresAt}
		return nil
	})
	if err != nil {
		return cronjob.ReportLease{}, err
	}
	if postErr != nil {
		return lease, postErr
	}
	return lease, nil
}

func (s *agentCronStore) FinishReport(ctx context.Context, request cronjob.ReportFinishRequest) (cronjob.Task, error) {
	var resultTask cronjob.Task
	if err := validateScope(request.Lease.Task.Scope); err != nil {
		return resultTask, err
	}
	if request.Lease.Token == "" || request.Lease.RunID == "" || request.Now.IsZero() {
		return resultTask, cronjob.NewError(cronjob.CodeInvalidArgument, "valid report lease and finish time are required")
	}
	scope := request.Lease.Task.Scope
	coordinator := scope.Coordinator()
	var postErr error
	err := s.watchCoordinator(ctx, coordinator, func(tx *redis.Tx, pipe redis.Pipeliner) error {
		resultTask = cronjob.Task{}
		postErr = nil
		record, err := s.loadReportLease(ctx, tx, coordinator, request.Lease.Token)
		if err != nil {
			return err
		}
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.Lease.Task.ID))
		if err != nil {
			return err
		}
		if record == nil {
			if stored == nil {
				postErr = cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
			} else {
				postErr = cronjob.NewError(cronjob.CodeConflict, "report lease is stale")
			}
			return nil
		}
		if stored == nil {
			s.releaseReportLease(ctx, pipe, *record)
			postErr = cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
			return nil
		}
		if record.Scope != scope || record.TaskID != request.Lease.Task.ID || !request.Now.Before(record.ExpiresAt) || stored.Task.Version != record.TaskVersion || stored.Task.Report == nil || stored.Task.Report.State != cronjob.DeliveryClaimed || stored.Task.Report.RunID != record.RunID || stored.Task.Report.TaskVersion != record.TaskVersion || stored.Task.Report.LeaseToken != record.Token || stored.Task.Report.Attempt != record.Attempt || request.Lease.Attempt != record.Attempt {
			postErr = cronjob.NewError(cronjob.CodeConflict, "report lease is stale")
			return nil
		}

		report := stored.Task.Report
		report.Receipt = agentCronMergeReceipt(report.Receipt, request.Receipt)
		report.LeaseToken = ""
		report.LeaseExpiresAt = time.Time{}
		report.LastError = agentCronTruncate(request.ErrorMessage, agentCronMaxErrorBytes)
		member := agentCronTaskMember(scope, stored.Task.ID)
		switch {
		case request.Suppressed:
			report.State = cronjob.DeliverySuppressed
			report.NextAttemptAt = time.Time{}
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
		case request.Delivered:
			report.State = cronjob.DeliveryDelivered
			report.NextAttemptAt = time.Time{}
			if report.Receipt == nil {
				report.Receipt = &cronjob.DeliveryReceipt{}
			}
			if report.Receipt.DeliveredAt.IsZero() {
				report.Receipt.DeliveredAt = request.Now
			}
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
		case agentCronReportExpired(stored.Task, request.Now):
			report.State = cronjob.DeliveryFailed
			report.NextAttemptAt = time.Time{}
			if report.LastError == "" {
				report.LastError = agentCronReportWindowError
			}
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
		case request.Exhausted || report.Attempt >= agentCronAutoReportAttempts+stored.ManualReportRetries:
			report.State = cronjob.DeliveryExhausted
			report.NextAttemptAt = time.Time{}
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
		default:
			report.State = cronjob.DeliveryPending
			report.NextAttemptAt = request.NextAttemptAt
			if report.NextAttemptAt.IsZero() || report.NextAttemptAt.Before(request.Now) {
				report.NextAttemptAt = request.Now
			}
			deadline := stored.Task.LatestResult.FinishedAt.Add(agentCronReportLifetime)
			if report.NextAttemptAt.After(deadline) {
				report.NextAttemptAt = deadline
			}
			pipe.ZAdd(ctx, s.reportsKey(coordinator), redis.Z{Score: agentCronTimeScore(report.NextAttemptAt), Member: member})
		}
		stored.Task.UpdatedAt = request.Now
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
		s.releaseReportLease(ctx, pipe, *record)
		resultTask = stored.Task
		return nil
	})
	if err != nil {
		return resultTask, err
	}
	if postErr != nil {
		return resultTask, postErr
	}
	return resultTask, nil
}

func (s *agentCronStore) recoverExpiredReportLeases(ctx context.Context, coordinator cronjob.Coordinator, now time.Time, limit int) error {
	tokens, err := s.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key: s.reportLeasesKey(coordinator), Start: agentCronNegativeInfinity,
		Stop: strconv.FormatInt(now.UnixMilli(), 10), ByScore: true, Count: int64(limit),
	}).Result()
	if err != nil {
		return agentCronPublicError(err)
	}
	for _, token := range tokens {
		err = s.watchCoordinator(ctx, coordinator, func(tx *redis.Tx, pipe redis.Pipeliner) error {
			record, loadErr := s.loadReportLease(ctx, tx, coordinator, token)
			if loadErr != nil {
				return loadErr
			}
			if record == nil {
				pipe.ZRem(ctx, s.reportLeasesKey(coordinator), token)
				return nil
			}
			if record.ExpiresAt.After(now) {
				return nil
			}
			stored, loadErr := loadAgentCronTask(ctx, tx, s.taskKey(record.Scope, record.TaskID))
			if loadErr != nil {
				return loadErr
			}
			if stored == nil {
				s.releaseReportLease(ctx, pipe, *record)
				return nil
			}
			if stored.Task.Version != record.TaskVersion || stored.Task.Report == nil || stored.Task.Report.State != cronjob.DeliveryClaimed || stored.Task.Report.LeaseToken != token || stored.Task.Report.RunID != record.RunID || stored.Task.Report.Attempt != record.Attempt {
				s.releaseReportLease(ctx, pipe, *record)
				return nil
			}
			report := stored.Task.Report
			report.LeaseToken = ""
			report.LeaseExpiresAt = time.Time{}
			report.LastError = "report delivery lease expired"
			member := agentCronTaskMember(stored.Task.Scope, stored.Task.ID)
			switch {
			case agentCronReportExpired(stored.Task, now):
				report.State = cronjob.DeliveryFailed
				report.NextAttemptAt = time.Time{}
				pipe.ZRem(ctx, s.reportsKey(coordinator), member)
			case report.Attempt >= agentCronAutoReportAttempts+stored.ManualReportRetries:
				report.State = cronjob.DeliveryExhausted
				report.NextAttemptAt = time.Time{}
				pipe.ZRem(ctx, s.reportsKey(coordinator), member)
			default:
				report.State = cronjob.DeliveryPending
				report.NextAttemptAt = now
				pipe.ZAdd(ctx, s.reportsKey(coordinator), redis.Z{Score: agentCronTimeScore(now), Member: member})
			}
			stored.Task.UpdatedAt = now
			data, marshalErr := marshalAgentCron(stored)
			if marshalErr != nil {
				return marshalErr
			}
			pipe.Set(ctx, s.taskKey(stored.Task.Scope, stored.Task.ID), data, 0)
			s.releaseReportLease(ctx, pipe, *record)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *agentCronStore) expirePendingReport(ctx context.Context, scope cronjob.Scope, taskID string, now time.Time) error {
	return s.watchCoordinator(ctx, scope.Coordinator(), func(tx *redis.Tx, pipe redis.Pipeliner) error {
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, taskID))
		if err != nil || stored == nil {
			return err
		}
		if stored.Task.Report == nil || stored.Task.Report.State != cronjob.DeliveryPending || !agentCronReportExpired(stored.Task, now) {
			return nil
		}
		stored.Task.Report.State = cronjob.DeliveryFailed
		stored.Task.Report.NextAttemptAt = time.Time{}
		stored.Task.Report.LastError = agentCronReportWindowError
		stored.Task.UpdatedAt = now
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		pipe.Set(ctx, s.taskKey(scope, taskID), data, 0)
		pipe.ZRem(ctx, s.reportsKey(scope.Coordinator()), agentCronTaskMember(scope, taskID))
		return nil
	})
}

func (s *agentCronStore) loadReportLease(ctx context.Context, cmd redis.Cmdable, coordinator cronjob.Coordinator, token string) (*agentCronReportLease, error) {
	data, err := cmd.Get(ctx, s.reportLeaseKey(coordinator, token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lease agentCronReportLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return nil, err
	}
	return &lease, nil
}

func (s *agentCronStore) releaseReportLease(ctx context.Context, pipe redis.Pipeliner, lease agentCronReportLease) {
	coordinator := lease.Scope.Coordinator()
	pipe.ZRem(ctx, s.reportLeasesKey(coordinator), lease.Token)
	pipe.Del(ctx, s.reportLeaseKey(coordinator, lease.Token))
}
