package util

import (
	"reflect"
)

type Result[T any] struct {
	e error
	v T
}

type compareResult[T comparable] Result[T]

func NewResult[T any](v T) Result[T] {
	return Result[T]{
		v: v,
	}
}

func NewErrorResult[T any](e error) Result[T] {
	return Result[T]{
		e: e,
	}
}

func WrapResult[T any](v T, e error) Result[T] {
	return Result[T]{
		e: e,
		v: v,
	}
}

func InResult[T comparable](r Result[T], e T) bool {
	return r.e == nil && reflect.DeepEqual(r.v, e)
}

func (r Result[T]) IsError() bool {
	return r.e != nil
}

func (r Result[T]) IsOk() bool {
	return r.e == nil
}

func (r Result[T]) Expect(msg string) T {
	if r.IsError() {
		panic(msg)
	}
	return r.v
}

func (r Result[T]) Unwrap() T {
	if r.IsError() {
		panic("get value from an error result")
	}
	return r.v
}

func (r Result[T]) UnwrapOr(v T) T {
	if r.IsError() {
		return v
	}
	return r.v
}

func (r Result[T]) Get() (T, error) {
	return r.v, r.e
}

func (r Result[T]) Or(r2 Result[T]) Result[T] {
	if r.IsError() {
		return r2
	}
	return r
}

func (r Result[T]) Then(fn func(T) T) Result[T] {
	if r.IsError() {
		return r
	}
	return NewResult(fn(r.v))
}

func (r Result[T]) Else(fn func(error) T) Result[T] {
	if r.IsError() {
		return NewResult(fn(r.e))
	}
	return r
}

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
