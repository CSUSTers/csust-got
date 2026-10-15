package session

import (
	"context"
	"errors"
)

// Recover retries durable intents and already-deleting DAGs without claiming idle
// active DAGs. It is safe to call at startup and between scheduled collections.
func (s *Service) Recover(ctx context.Context) error {
	return s.maintain(ctx, false)
}

// Collect also claims idle active DAGs. The caller schedules it at local 02:00.
func (s *Service) Collect(ctx context.Context) error {
	return s.maintain(ctx, true)
}

func (s *Service) maintain(ctx context.Context, collectExpired bool) error {
	op, done, err := s.track(ctx)
	if err != nil {
		return err
	}
	defer done()
	catalog, cancel := context.WithTimeout(op, s.options.OperationTimeout)
	scopes, err := s.repo.Scopes(catalog)
	cancel()
	if err != nil && !maintenancePureCorruption(err) {
		return err
	}
	var failures []error
	if err != nil {
		failures = append(failures, err)
	}
	scopeTimeout := s.options.OperationTimeout
	if collectExpired {
		scopeTimeout = s.options.CollectTimeout
	}
	for _, scope := range scopes {
		if err = op.Err(); err != nil {
			return errors.Join(err, errors.Join(failures...))
		}
		scopeCtx, scopeCancel := context.WithTimeout(op, scopeTimeout)
		err = s.maintainScope(scopeCtx, scope, collectExpired)
		scopeCancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(op.Err(), errors.Join(failures...))
}

// maintainScope takes the scope lock once for intent recovery, once to claim or read
// deleting DAGs, and then separately for each DAG removal so loads and commits interleave.
func (s *Service) maintainScope(ctx context.Context, scope Scope, collectExpired bool) error {
	var recoveryErrors []error
	err := s.files.WithScopeLock(ctx, scope, func(files *ScopeFiles) error {
		var err error
		recoveryErrors, err = s.recoverIntents(ctx, files, scope)
		return err
	})
	if err != nil {
		return errors.Join(err, errors.Join(recoveryErrors...))
	}
	var deletions []Deletion
	err = s.files.WithScopeLock(ctx, scope, func(*ScopeFiles) error {
		var err error
		if collectExpired {
			deletions, err = s.repo.ClaimDeleting(ctx, scope, s.options.TTL)
		} else {
			deletions, err = s.repo.Deleting(ctx, scope)
		}
		if err != nil && !maintenancePureCorruption(err) {
			return err
		}
		if err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
		return ctx.Err()
	})
	if err != nil {
		return errors.Join(err, errors.Join(recoveryErrors...))
	}
	for _, deletion := range deletions {
		if err = ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(recoveryErrors...))
		}
		err = s.files.WithScopeLock(ctx, scope, func(files *ScopeFiles) error {
			return s.removeDeletion(ctx, files, scope, deletion)
		})
		if err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	return errors.Join(ctx.Err(), errors.Join(recoveryErrors...))
}

func (s *Service) recoverIntents(ctx context.Context, files *ScopeFiles, scope Scope) ([]error, error) {
	intents, err := s.repo.Pending(ctx, scope)
	if err != nil && !maintenancePureCorruption(err) {
		return nil, err
	}
	var recoveryErrors []error
	if err != nil {
		recoveryErrors = append(recoveryErrors, err)
	}
	for _, intent := range intents {
		if err = ctx.Err(); err != nil {
			return recoveryErrors, err
		}
		if intent.Status == "published" {
			err = s.repo.FinishIntent(ctx, scope, intent)
		} else {
			err = s.abortIntent(ctx, files, scope, intent)
		}
		if err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	return recoveryErrors, ctx.Err()
}

func maintenancePureCorruption(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !maintenancePureCorruption(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return maintenancePureCorruption(cause)
	}
	return errors.Is(err, ErrCorrupt)
}

func (s *Service) removeDeletion(ctx context.Context, files *ScopeFiles, scope Scope, deletion Deletion) error {
	if !ValidID(deletion.DAGID) || !ValidID(deletion.Generation) {
		return ErrCorrupt
	}
	var removeErrors []error
	for _, n := range deletion.Nodes {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(removeErrors...))
		}
		if n.Scope != scope || n.Ref.DAGID != deletion.DAGID {
			removeErrors = append(removeErrors, ErrCorrupt)
		}
	}
	for _, i := range deletion.Intents {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(removeErrors...))
		}
		if i.Node.Scope != scope || i.Node.Ref.DAGID != deletion.DAGID || i.Lease.Generation != deletion.Generation {
			removeErrors = append(removeErrors, ErrCorrupt)
		}
	}
	if len(removeErrors) > 0 {
		return errors.Join(removeErrors...)
	}
	for _, n := range deletion.Nodes {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(removeErrors...))
		}
		if err := files.Remove(n); err != nil {
			removeErrors = append(removeErrors, err)
		}
	}
	for _, i := range deletion.Intents {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(removeErrors...))
		}
		if err := files.Remove(i.Node); err != nil {
			removeErrors = append(removeErrors, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, errors.Join(removeErrors...))
	}
	if len(removeErrors) > 0 {
		return errors.Join(removeErrors...)
	}
	// If an unknown file prevents rmdir, retain the tombstone for diagnosis/retry.
	if err := files.RemoveEmptyDAG(deletion.DAGID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.repo.FinishDelete(ctx, scope, deletion)
}
