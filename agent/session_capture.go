package agentv3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var (
	errSessionCaptureReused           = errors.New("session capture must be used for only one invocation")
	errSessionCaptureCyclicValue      = errors.New("session capture: cyclic")
	errSessionCaptureUnexportedField  = errors.New("session capture: unexported field in")
	errSessionCaptureUnsupportedValue = errors.New("session capture: unsupported")
)

type sessionCaptureContextKey struct{}

// SessionCapture records one invocation, independently of a shared agent.
type SessionCapture struct {
	mu      sync.Mutex
	result  SessionCaptureResult
	started bool
	done    chan struct{}
}

// SessionCaptureResult holds invocation snapshots and newly generated messages.
// Input is the invocation snapshot; ModelInput is the sanitized/directive baseline.
// Messages contains only new model/tool messages, with framework guidance separate.
type SessionCaptureResult struct {
	Input      []*schema.Message
	ModelInput []*schema.Message
	Messages   []*schema.Message
	Guidance   []SessionCaptureGuidance
	Complete   bool
	Err        error
}

// SessionCaptureGuidance records framework guidance at an offset in Messages.
// BeforeMessage is an offset in Messages, not in the caller's input history.
type SessionCaptureGuidance struct {
	BeforeMessage int
	Message       *schema.Message
}

// NewSessionCapture creates a recorder for one invocation.
func NewSessionCapture() *SessionCapture {
	return &SessionCapture{done: make(chan struct{})}
}

// WithSessionCapture sets the recorder; nil disables inherited capture, including in subagents.
func WithSessionCapture(ctx context.Context, capture *SessionCapture) context.Context {
	return context.WithValue(ctx, sessionCaptureContextKey{}, capture)
}

func sessionCaptureFromContext(ctx context.Context) *SessionCapture {
	capture, _ := ctx.Value(sessionCaptureContextKey{}).(*SessionCapture)
	return capture
}

// Done closes before the output stream closes, including on incomplete exits.
func (c *SessionCapture) Done() <-chan struct{} {
	return c.done
}

// Snapshot returns detached messages. Complete does not attest Telegram delivery;
// integration must also require a successful delivery receipt before committing.
func (c *SessionCapture) Snapshot() SessionCaptureResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	source := c.result
	source.Err = nil
	result, err := cloneSessionCaptureValue(reflect.ValueOf(source), make(map[sessionCaptureVisit]bool))
	if err != nil {
		return SessionCaptureResult{Err: err}
	}
	snapshot := result.Interface().(SessionCaptureResult)
	snapshot.Err = c.result.Err
	return snapshot
}

func (c *SessionCapture) begin(input []*schema.Message) ([]*schema.Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		c.result.Err = errSessionCaptureReused
		c.result.Complete = false
		return input, false
	}
	c.started = true
	c.result.Input, c.result.Err = cloneSessionCaptureMessages(input)
	if c.result.Err != nil {
		return input, true
	}
	loopInput, err := cloneSessionCaptureMessages(c.result.Input)
	c.result.Err = err
	if err != nil {
		return input, true
	}
	return loopInput, true
}

func (c *SessionCapture) recordModelInput(input []*schema.Message) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.result.Err == nil {
		c.result.ModelInput, c.result.Err = cloneSessionCaptureMessages(input)
	}
}

func (c *SessionCapture) record(message *schema.Message, guidance bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.result.Err != nil {
		return
	}
	messages, err := cloneSessionCaptureMessages([]*schema.Message{message})
	if err != nil {
		c.result.Err = err
		return
	}
	if guidance {
		c.result.Guidance = append(c.result.Guidance, SessionCaptureGuidance{
			BeforeMessage: len(c.result.Messages), Message: messages[0],
		})
	} else {
		c.result.Messages = append(c.result.Messages, messages[0])
	}
}

func (c *SessionCapture) complete(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result.Complete = c.result.Err == nil && ctx.Err() == nil
}

type sessionCaptureSubAgentTool struct {
	tool.InvokableTool
}

func (t *sessionCaptureSubAgentTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	return t.InvokableTool.InvokableRun(WithSessionCapture(ctx, nil), args, opts...)
}

func cloneSessionCaptureMessages(messages []*schema.Message) ([]*schema.Message, error) {
	value, err := cloneSessionCaptureValue(reflect.ValueOf(messages), make(map[sessionCaptureVisit]bool))
	if err != nil {
		return nil, err
	}
	return value.Interface().([]*schema.Message), nil
}

type sessionCaptureVisit struct {
	typ reflect.Type
	ptr uintptr
}

// Preserve Go types and non-JSON schema fields as well as all nested metadata.
// Unsupported or cyclic values invalidate capture rather than being discarded.
func cloneSessionCaptureValue(value reflect.Value, active map[sessionCaptureVisit]bool) (reflect.Value, error) {
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := sessionCaptureVisit{typ: value.Type(), ptr: uintptr(value.UnsafePointer())}
		if active[visit] {
			return reflect.Value{}, fmt.Errorf("%w %s", errSessionCaptureCyclicValue, value.Type())
		}
		active[visit] = true
		defer delete(active, visit)
	}
	if value.Type() == reflect.TypeFor[*schema.ParamsOneOf]() {
		// ToolInfo's SDK codec preserves both private parameter representations.
		// Keep Extra outside this JSON path so its Go runtime values stay intact.
		encoded, err := json.Marshal(&schema.ToolInfo{ParamsOneOf: value.Interface().(*schema.ParamsOneOf)})
		if err != nil {
			return reflect.Value{}, err
		}
		var info schema.ToolInfo
		if err = json.Unmarshal(encoded, &info); err != nil {
			return reflect.Value{}, err
		}
		restored, err := json.Marshal(&info)
		if err != nil {
			return reflect.Value{}, err
		}
		if !bytes.Equal(encoded, restored) {
			return reflect.Value{}, fmt.Errorf("%w parameter schema JSON round trip", errSessionCaptureUnsupportedValue)
		}
		return reflect.ValueOf(info.ParamsOneOf), nil
	}
	clone := reflect.New(value.Type()).Elem()
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return clone, nil
		}
		element, err := cloneSessionCaptureValue(value.Elem(), active)
		if err != nil {
			return reflect.Value{}, err
		}
		clone.Set(element)
	case reflect.Pointer:
		element, err := cloneSessionCaptureValue(value.Elem(), active)
		if err != nil {
			return reflect.Value{}, err
		}
		clone.Set(reflect.New(value.Type().Elem()))
		clone.Elem().Set(element)
	case reflect.Map:
		clone.Set(reflect.MakeMapWithSize(value.Type(), value.Len()))
		iter := value.MapRange()
		for iter.Next() {
			key, err := cloneSessionCaptureValue(iter.Key(), active)
			if err != nil {
				return reflect.Value{}, err
			}
			element, err := cloneSessionCaptureValue(iter.Value(), active)
			if err != nil {
				return reflect.Value{}, err
			}
			clone.SetMapIndex(key, element)
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice {
			clone.Set(reflect.MakeSlice(value.Type(), value.Len(), value.Len()))
		}
		for i := range value.Len() {
			element, err := cloneSessionCaptureValue(value.Index(i), active)
			if err != nil {
				return reflect.Value{}, err
			}
			clone.Index(i).Set(element)
		}
	case reflect.Struct:
		for i := range value.NumField() {
			if value.Type().Field(i).PkgPath != "" {
				return reflect.Value{}, fmt.Errorf("%w %s", errSessionCaptureUnexportedField, value.Type())
			}
			field, err := cloneSessionCaptureValue(value.Field(i), active)
			if err != nil {
				return reflect.Value{}, err
			}
			clone.Field(i).Set(field)
		}
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("%w %s", errSessionCaptureUnsupportedValue, value.Type())
	default:
		clone.Set(value)
	}
	return clone, nil
}
