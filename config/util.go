package config

import (
	"errors"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// DispatchableType used for mapstructure hook
type DispatchableType interface {
	From(src reflect.Value) (any, error)
}

// DispatchFor create mapstructure hook
func DispatchFor() mapstructure.DecodeHookFuncValue {
	return func(in reflect.Value, to reflect.Value) (any, error) {
		if in.Kind() == reflect.Pointer && in.IsNil() {
			return nil, nil
		}
		if to.Type().Implements(reflect.TypeFor[DispatchableType]()) {
			fn := to.Interface().(DispatchableType)
			return fn.From(in)
		}

		return in.Interface(), nil
	}
}

// JoinableString joins an array of strings
type JoinableString string

var _ DispatchableType = JoinableString("")

// ErrUnsupportedType is the error returned when the type is not supported
var ErrUnsupportedType = errors.New("unsupported type")

// From implements DispatchableType
func (p JoinableString) From(src reflect.Value) (any, error) {
	oriKind := src.Kind()
	kind := oriKind
	for kind == reflect.Pointer {
		if src.IsNil() {
			return nil, nil
		}
		src = src.Elem()
		kind = src.Kind()
	}
	switch kind {
	case reflect.String:
		return JoinableString(src.String()), nil
	case reflect.Array, reflect.Slice:
		var parts []string
	loop:
		for i := range src.Len() {
			v := src.Index(i)
			switch v.Kind() {
			case reflect.String:
				parts = append(parts, v.String())
			case reflect.Interface, reflect.Pointer:
				if v.IsNil() {
					continue loop
				}
				v = v.Elem()
				if v.Kind() == reflect.String {
					parts = append(parts, v.String())
					continue loop
				}
				fallthrough
			default:
				return nil, ErrUnsupportedType
			}
		}
		return JoinableString(strings.Join(parts, "")), nil
	default:
		return nil, ErrUnsupportedType
	}
}

func (p JoinableString) String() string {
	return string(p)
}
