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
	op, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	scopes, err := s.repo.Scopes(op)
	if err != nil {
		return err
	}
	var failures []error
	for _, scope := range scopes {
		err = s.files.WithScopeLock(op, scope, func(files *ScopeFiles) error {
			intents, err := s.repo.Pending(op, scope)
			if err != nil {
				return err
			}
			var recoveryErrors []error
			for _, intent := range intents {
				if intent.Status == "published" {
					if err = s.repo.FinishIntent(op, scope, intent); err != nil {
						recoveryErrors = append(recoveryErrors, err)
					}
				} else if err = s.compensate(files, scope, intent); err != nil {
					recoveryErrors = append(recoveryErrors, err)
				}
			}
			var deletions []Deletion
			if collectExpired {
				deletions, err = s.repo.ClaimDeleting(op, scope, s.options.TTL)
			} else {
				deletions, err = s.repo.Deleting(op, scope)
			}
			if err != nil {
				return errors.Join(err, errors.Join(recoveryErrors...))
			}
			for _, deletion := range deletions {
				var removeErrors []error
				if !ValidID(deletion.DAGID) || !ValidID(deletion.Generation) {
					recoveryErrors = append(recoveryErrors, ErrCorrupt)
					continue
				}
				for _, n := range deletion.Nodes {
					if n.Scope != scope || n.Ref.DAGID != deletion.DAGID {
						removeErrors = append(removeErrors, ErrCorrupt)
					}
				}
				for _, i := range deletion.Intents {
					if i.Node.Scope != scope || i.Node.Ref.DAGID != deletion.DAGID || i.Lease.Generation != deletion.Generation {
						removeErrors = append(removeErrors, ErrCorrupt)
					}
				}
				if len(removeErrors) > 0 {
					recoveryErrors = append(recoveryErrors, errors.Join(removeErrors...))
					continue
				}
				for _, n := range deletion.Nodes {
					if err = files.Remove(n); err != nil {
						removeErrors = append(removeErrors, err)
					}
				}
				for _, i := range deletion.Intents {
					if err = files.Remove(i.Node); err != nil {
						removeErrors = append(removeErrors, err)
					}
				}
				if len(removeErrors) > 0 {
					recoveryErrors = append(recoveryErrors, errors.Join(removeErrors...))
					continue
				}
				// If an unknown file prevents rmdir, retain the tombstone for diagnosis/retry.
				if err = files.RemoveEmptyDAG(deletion.DAGID); err != nil {
					recoveryErrors = append(recoveryErrors, err)
					continue
				}
				if err = s.repo.FinishDelete(op, scope, deletion); err != nil {
					recoveryErrors = append(recoveryErrors, err)
				}
			}
			return errors.Join(recoveryErrors...)
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
