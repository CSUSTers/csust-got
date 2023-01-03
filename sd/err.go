package sd

import "errors"

// sd error
var (
	ErrServerNotConfigured = errors.New("server not configured")
	ErrServerNotAvailable  = errors.New("server not available")
	ErrConfigKeyNotSupport = errors.New("config key not support")
	ErrConfigIsInvalid     = errors.New("config is invalid")
	ErrRequestNotOK        = errors.New("request not ok")
)
