package cronjob

import (
	"context"
	"time"
)

// Scope identifies the bot, platform, and chat that own a task.
type Scope struct {
	Bot      string
	Platform string
	ChatID   int64
}

// Coordinator identifies the bot and platform responsible for task execution.
type Coordinator struct {
	Bot      string
	Platform string
}

// Coordinator returns the execution coordinator for the scope.
func (s Scope) Coordinator() Coordinator {
	return Coordinator{Bot: s.Bot, Platform: s.Platform}
}

// Actor identifies a user acting within a task scope.
type Actor struct {
	Scope  Scope
	UserID int64
}

// RunKind identifies whether a run is scheduled or manually retried.
type RunKind string

// RunScheduled and RunRetry identify the source of a run.
const (
	RunScheduled RunKind = "scheduled"
	RunRetry     RunKind = "retry"
)

// Outcome identifies the terminal result of a run.
type Outcome string

// OutcomeSucceeded and the other outcomes describe terminal run results.
const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeSkipped   Outcome = "skipped"
)

// RetryStatus describes whether and how a run can be retried.
type RetryStatus string

// RetryUnavailable and the other statuses describe retry availability.
const (
	RetryUnavailable RetryStatus = "unavailable"
	RetryAvailable   RetryStatus = "available"
	RetryQueued      RetryStatus = "queued"
	RetryConsumed    RetryStatus = "consumed"
	RetryExhausted   RetryStatus = "exhausted"
)

// DeliveryState describes report delivery progress.
type DeliveryState string

// DeliveryPending and the other states describe report delivery progress.
const (
	DeliveryPending    DeliveryState = "pending"
	DeliveryClaimed    DeliveryState = "claimed"
	DeliveryDelivered  DeliveryState = "delivered"
	DeliveryFailed     DeliveryState = "failed"
	DeliveryExhausted  DeliveryState = "exhausted"
	DeliverySuppressed DeliveryState = "suppressed"
)

// Run records the active execution lease and retry lineage for a task.
type Run struct {
	RunID          string
	Kind           RunKind
	TaskVersion    int64
	ScheduledAt    time.Time
	StartedAt      time.Time
	RetryOfRunID   string
	RetryCount     int
	LeaseToken     string
	RunTimeoutAt   time.Time
	LeaseExpiresAt time.Time
}

// Attachment describes an output artifact produced by a run.
type Attachment struct {
	Name        string
	ContentType string
	URL         string
}

// ExecutionResult records the terminal outcome and output of a run.
type ExecutionResult struct {
	RunID               string
	Kind                RunKind
	TaskVersion         int64
	ScheduledAt         time.Time
	StartedAt           time.Time
	FinishedAt          time.Time
	Outcome             Outcome
	RetryOfRunID        string
	RetryCount          int
	RetryStatus         RetryStatus
	Text                string
	Format              string
	Attachments         []Attachment
	ErrorCode           string
	ErrorMessage        string
	SideEffectsPossible bool
}

// DeliveryReceipt records messages successfully delivered for a report.
type DeliveryReceipt struct {
	MessageIDs  []int
	DeliveredAt time.Time
}

// Report tracks delivery of an execution result.
type Report struct {
	RunID          string
	TaskVersion    int64
	State          DeliveryState
	Attempt        int
	NextAttemptAt  time.Time
	LeaseToken     string
	LeaseExpiresAt time.Time
	LastError      string
	Receipt        *DeliveryReceipt
}

// Task is a persisted scheduled agent task and its latest execution state.
type Task struct {
	ID              string
	Scope           Scope
	CreatorID       int64
	SourceAgent     string
	ChatType        string
	ThreadID        int64
	SourceMessageID int
	Cron            string
	Timezone        string
	Prompt          string
	Version         int64
	NextRunAt       time.Time
	ActiveRun       *Run
	LatestResult    *ExecutionResult
	Report          *Report
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateRequest contains the inputs and limits for creating a task.
type CreateRequest struct {
	Actor           Actor
	SourceAgent     string
	ChatType        string
	ThreadID        int64
	SourceMessageID int
	Cron            string
	Timezone        string
	Prompt          string
	NextRunAt       time.Time
	Now             time.Time
	MaxTasksPerChat int
}

// CreateResult returns the created task and whether it was deduplicated.
type CreateResult struct {
	Task         Task
	Deduplicated bool
}

// ListRequest selects one page of tasks within a scope.
type ListRequest struct {
	Scope  Scope
	Cursor string
	Limit  int
}

// Page contains a page of tasks and an optional continuation cursor.
type Page struct {
	Tasks      []Task
	NextCursor string
}

// GetRequest identifies a task within a scope.
type GetRequest struct {
	Scope  Scope
	TaskID string
}

// UpdateRequest applies a version-fenced task update.
type UpdateRequest struct {
	Actor           Actor
	TaskID          string
	ExpectedVersion int64
	Cron            *string
	Prompt          *string
	NextRunAt       time.Time
	Now             time.Time
}

// DeleteRequest applies a version-fenced task deletion.
type DeleteRequest struct {
	Actor           Actor
	TaskID          string
	ExpectedVersion int64
	Now             time.Time
}

// RetryMode identifies whether execution or report delivery is retried.
type RetryMode string

// RetryExecution and RetryReportOnly identify retry scope.
const (
	RetryExecution  RetryMode = "execution"
	RetryReportOnly RetryMode = "report_only"
)

// RetryRequest requests a version-fenced retry of a completed run.
type RetryRequest struct {
	Actor           Actor
	TaskID          string
	ExpectedVersion int64
	RunID           string
	Now             time.Time
	MaxRetries      int
}

// RetryResult returns the updated task and selected retry mode.
type RetryResult struct {
	Task Task
	Mode RetryMode
}

// DueRequest bounds a scan for tasks due under a coordinator.
type DueRequest struct {
	Coordinator Coordinator
	Now         time.Time
	Limit       int
}

// Candidate identifies a due task version eligible to be claimed.
type Candidate struct {
	Scope        Scope
	TaskID       string
	TaskVersion  int64
	Kind         RunKind
	ScheduledAt  time.Time
	RetryOfRunID string
	RetryCount   int
}

// ClaimRequest contains execution lease timing and concurrency limits.
type ClaimRequest struct {
	Candidate      Candidate
	RunID          string
	Now            time.Time
	RunTimeout     time.Duration
	GracePeriod    time.Duration
	MaxConcurrency int
	ChatCooldown   time.Duration
}

// Lease grants time-bounded execution ownership of a task run.
type Lease struct {
	Task         Task
	RunID        string
	Token        string
	Kind         RunKind
	TaskVersion  int64
	ScheduledAt  time.Time
	RetryOfRunID string
	RetryCount   int
	RunTimeoutAt time.Time
	ExpiresAt    time.Time
}

// FinishRequest atomically records a run result, report, and next run time.
type FinishRequest struct {
	Lease     Lease
	Result    ExecutionResult
	Report    Report
	NextRunAt time.Time
	Now       time.Time
}

// RecoverRequest bounds recovery of expired execution leases.
type RecoverRequest struct {
	Coordinator Coordinator
	Now         time.Time
	Limit       int
}

// RecoverResult reports how many expired leases were recovered.
type RecoverResult struct {
	Recovered int
}

// ReportsRequest bounds a scan for reports ready for delivery.
type ReportsRequest struct {
	Coordinator Coordinator
	Now         time.Time
	Limit       int
}

// ReportCandidate identifies a due report version eligible to be claimed.
type ReportCandidate struct {
	Scope       Scope
	TaskID      string
	TaskVersion int64
	RunID       string
	Attempt     int
	DueAt       time.Time
}

// ClaimReportRequest contains report delivery lease timing.
type ClaimReportRequest struct {
	Candidate   ReportCandidate
	Now         time.Time
	Timeout     time.Duration
	GracePeriod time.Duration
}

// ReportLease grants time-bounded delivery ownership of a report.
type ReportLease struct {
	Task      Task
	RunID     string
	Token     string
	Attempt   int
	ExpiresAt time.Time
}

// ReportFinishRequest records a delivery attempt and its next state.
type ReportFinishRequest struct {
	Lease         ReportLease
	Now           time.Time
	Delivered     bool
	Receipt       *DeliveryReceipt
	ErrorMessage  string
	NextAttemptAt time.Time
	Exhausted     bool
	Suppressed    bool
}

// Store persists cron tasks, leases, results, and report deliveries.
type Store interface {
	Create(context.Context, CreateRequest) (CreateResult, error)
	List(context.Context, ListRequest) (Page, error)
	Get(context.Context, GetRequest) (Task, error)
	Update(context.Context, UpdateRequest) (Task, error)
	Delete(context.Context, DeleteRequest) error
	Retry(context.Context, RetryRequest) (RetryResult, error)
	Due(context.Context, DueRequest) ([]Candidate, error)
	Claim(context.Context, ClaimRequest) (Lease, error)
	Finish(context.Context, FinishRequest) (Task, error)
	Recover(context.Context, RecoverRequest) (RecoverResult, error)
	Reports(context.Context, ReportsRequest) ([]ReportCandidate, error)
	ClaimReport(context.Context, ClaimReportRequest) (ReportLease, error)
	FinishReport(context.Context, ReportFinishRequest) (Task, error)
}
