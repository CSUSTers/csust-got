package orm

import "errors"

// redis process errors
var (
	ErrWrongType    = errors.New("wrong type")
	ErrMessageIsNil = errors.New("message is nil")
)
