package util

import (
	"reflect"
)

// Result is a type contains result.
type Result[T any] struct {
	e error
	v T
}

// CompareResult is a comparable version of Result.
type CompareResult[T comparable] Result[T]

// NewResult returns a new Result with value.
func NewResult[T any](v T) Result[T] {
	return Result[T]{
		v: v,
	}
}

// NewErrorResult returns a new Result with error.
func NewErrorResult[T any](e error) Result[T] {
	return Result[T]{
		e: e,
	}
}

// WrapResult wrap go style error to Result.
func WrapResult[T any](v T, e error) Result[T] {
	return Result[T]{
		e: e,
		v: v,
	}
}

// InResult returns true if the value equals Result.
func InResult[T comparable](r Result[T], e T) bool {
	return r.e == nil && reflect.DeepEqual(r.v, e)
}

// IsError returns true if Result is error.
func (r Result[T]) IsError() bool {
	return r.e != nil
}

// IsOk returns true if Result is ok.
func (r Result[T]) IsOk() bool {
	return r.e == nil
}

// Expect returns value of Result or panic with a msg.
func (r Result[T]) Expect(msg string) T {
	if r.IsError() {
		panic(msg)
	}
	return r.v
}

// Expect returns value of Result or panic.
func (r Result[T]) Unwrap() T {
	if r.IsError() {
		panic("get value from an error result")
	}
	return r.v
}

// Unwrap returns value of Result or another.
func (r Result[T]) UnwrapOr(v T) T {
	if r.IsError() {
		return v
	}
	return r.v
}

// Get returns go-style error handling
func (r Result[T]) Get() (T, error) {
	return r.v, r.e
}

// Or returns self if it's ok or return another.
func (r Result[T]) Or(r2 Result[T]) Result[T] {
	if r.IsError() {
		return r2
	}
	return r
}

// Then deal with a function and returns a Result.
func (r Result[T]) Then(fn func(T) T) Result[T] {
	if r.IsError() {
		return r
	}
	return NewResult(fn(r.v))
}

// Else deal with a error-handling function and returns a Result.
func (r Result[T]) Else(fn func(error) T) Result[T] {
	if r.IsError() {
		return NewResult(fn(r.e))
	}
	return r
}

// ThenOr returns a Result map with a function if it's ok, or return a wrapping value.
func (r Result[T]) ThenOr(fn func(T) T, o T) Result[T] {
	if r.IsError() {
		return NewResult(o)
	}
	return NewResult(fn(r.v))
}

func (r Result[T]) ThenElse(fn func(error) Result[T]) Result[T] {
	if r.IsError() {
		return fn(r.e)
	}
	return r
}

func (r Result[T]) Map(fn func(T) T) T {
	if r.IsError() {
		return r.v
	}
	return fn(r.v)
}

func (r Result[T]) MapOr(fn func(T) T, d T) T {
	if r.IsError() {
		return d
	}
	return fn(r.v)
}

func (r Result[T]) MapOrElse(fn_ok func(T) T, fn_fail func(error) T) T {
	if r.IsError() {
		return fn_fail(r.e)
	}
	return fn_ok(r.v)
}

func (r Result[T]) Do(fn func(T)) Result[T] {
	if !r.IsError() {
		fn(r.v)
	}
	return r
}

func (r Result[T]) DoError(fn func(error)) Result[T] {
	if r.IsError() {
		fn(r.e)
	}
	return r
}
