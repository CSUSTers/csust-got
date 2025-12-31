package orm

import "errors"

// redis process errors
var (
	ErrWrongType      = errors.New("wrong type")
	ErrMessageIsNil   = errors.New("message is nil")
	ErrClientNotFound = errors.New("AI client not found")
)
