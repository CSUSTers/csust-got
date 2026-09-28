package cronjob

import "fmt"

// ErrorCode identifies a stable cron operation failure category.
type ErrorCode string

// CodeInvalidArgument and the other codes classify cron operation failures.
const (
	CodeInvalidArgument ErrorCode = "invalid_argument"
	CodeNotFound        ErrorCode = "not_found"
	CodeConflict        ErrorCode = "conflict"
	CodeForbidden       ErrorCode = "forbidden"
	CodeRateLimited     ErrorCode = "rate_limited"
	CodeQuotaExceeded   ErrorCode = "quota_exceeded"
	CodeUnavailable     ErrorCode = "unavailable"
)

// Error reports a cron operation failure with a stable code.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is matches cron errors by code.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e.Code == other.Code
}

// NewError creates a coded cron error with the supplied message.
func NewError(code ErrorCode, message string) error {
	return &Error{Code: code, Message: message}
}

var (
	// ErrInvalidArgument matches invalid arguments.
	ErrInvalidArgument = &Error{Code: CodeInvalidArgument}
	// ErrNotFound matches missing tasks.
	ErrNotFound = &Error{Code: CodeNotFound}
	// ErrConflict matches version or state conflicts.
	ErrConflict = &Error{Code: CodeConflict}
	// ErrForbidden matches denied operations.
	ErrForbidden = &Error{Code: CodeForbidden}
	// ErrRateLimited matches rate-limited operations.
	ErrRateLimited = &Error{Code: CodeRateLimited}
	// ErrQuotaExceeded matches task quota failures.
	ErrQuotaExceeded = &Error{Code: CodeQuotaExceeded}
	// ErrUnavailable matches temporarily unavailable operations.
	ErrUnavailable = &Error{Code: CodeUnavailable}
)
