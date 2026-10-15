package agentv3

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

const sessionContextEstimateMethod = "text-runs-media-budget-v1"

var errSessionContextEstimate = errors.New("session context estimate: unsupported input")

type sessionContextEstimate struct {
	Tokens int64
	Capped bool
	Method string
}

type sessionTokenCounter struct {
	sessionContextEstimate
	ceiling int64
	limit   int64
}

func newSessionTokenCounter(limit int64) *sessionTokenCounter {
	ceiling := limit
	if limit < math.MaxInt64 {
		ceiling++
	}
	return &sessionTokenCounter{sessionContextEstimate: sessionContextEstimate{Method: sessionContextEstimateMethod}, ceiling: ceiling, limit: limit}
}

func (c *sessionTokenCounter) add(n int64) {
	if n > c.ceiling-c.Tokens {
		c.Tokens, c.Capped = c.ceiling, true
		return
	}
	c.Tokens += n
	if c.limit < math.MaxInt64 && c.Tokens == c.ceiling {
		c.Capped = true
	}
}

func (c *sessionTokenCounter) text(text string) {
	j := sessionJSONTokenCounter{counter: c}
	j.write(text)
	j.finish()
}

// Preview borrows input read-only, just as sanitize/directive construction does.
// Capture's invocation and model snapshots remain separate, detached baselines.
func (a *CustomAgent) estimateSessionContext(ctx context.Context, input []*schema.Message, limit int64) (sessionContextEstimate, error) {
	c := newSessionTokenCounter(limit)
	if limit <= 0 {
		return c.sessionContextEstimate, fmt.Errorf("%w: limit must be positive", errSessionContextEstimate)
	}
	if err := ctx.Err(); err != nil {
		return c.sessionContextEstimate, err
	}
	if a.sessionToolEstimateErr != nil {
		return c.sessionContextEstimate, a.sessionToolEstimateErr
	}
	c.add(32)
	c.add(a.sessionToolEstimate.Tokens)
	c.Capped = c.Capped || a.sessionToolEstimate.Capped
	for _, message := range a.previewSessionModelInput(ctx, input) {
		if err := ctx.Err(); err != nil {
			return c.sessionContextEstimate, err
		}
		if err := c.message(message); err != nil {
			return c.sessionContextEstimate, err
		}
	}
	return c.sessionContextEstimate, nil
}

func (c *sessionTokenCounter) message(m *schema.Message) error {
	c.add(16)
	for _, text := range []string{m.Content, m.Name, m.ToolCallID, m.ToolName, m.ReasoningContent} {
		c.text(text)
	}
	for _, call := range m.ToolCalls {
		for _, text := range []string{call.ID, call.Type, call.Function.Name, call.Function.Arguments} {
			c.text(text)
		}
		if err := c.metadata(call.Extra); err != nil {
			return err
		}
	}
	for _, part := range m.MultiContent {
		c.text(part.Text)
		var extra map[string]any
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			if part.ImageURL != nil || part.AudioURL != nil || part.VideoURL != nil || part.FileURL != nil {
				return unsupportedSessionPart(part.Type)
			}
		case schema.ChatMessagePartTypeImageURL:
			if part.ImageURL == nil || part.AudioURL != nil || part.VideoURL != nil || part.FileURL != nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(4096)
			extra = part.ImageURL.Extra
		case schema.ChatMessagePartTypeAudioURL:
			if part.AudioURL == nil || part.ImageURL != nil || part.VideoURL != nil || part.FileURL != nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(8192)
			extra = part.AudioURL.Extra
		case schema.ChatMessagePartTypeVideoURL:
			if part.VideoURL == nil || part.ImageURL != nil || part.AudioURL != nil || part.FileURL != nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(16384)
			extra = part.VideoURL.Extra
		case schema.ChatMessagePartTypeFileURL:
			if part.FileURL == nil || part.ImageURL != nil || part.AudioURL != nil || part.VideoURL != nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(8192)
			c.text(part.FileURL.Name)
			extra = part.FileURL.Extra
		default:
			return unsupportedSessionPart(part.Type)
		}
		if err := c.metadata(extra); err != nil {
			return err
		}
	}
	for _, part := range m.UserInputMultiContent {
		c.text(part.Text)
		var common *schema.MessagePartCommon
		populated := 0
		for _, present := range []bool{part.Image != nil, part.Audio != nil, part.Video != nil, part.File != nil, part.ToolSearchResult != nil} {
			if present {
				populated++
			}
		}
		if part.Type == schema.ChatMessagePartTypeText && populated != 0 || part.Type != schema.ChatMessagePartTypeText && populated != 1 {
			return unsupportedSessionPart(part.Type)
		}
		switch part.Type {
		case schema.ChatMessagePartTypeText:
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(4096)
			common = &part.Image.MessagePartCommon
		case schema.ChatMessagePartTypeAudioURL:
			if part.Audio == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(8192)
			common = &part.Audio.MessagePartCommon
		case schema.ChatMessagePartTypeVideoURL:
			if part.Video == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(16384)
			common = &part.Video.MessagePartCommon
		case schema.ChatMessagePartTypeFileURL:
			if part.File == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(8192)
			c.text(part.File.Name)
			common = &part.File.MessagePartCommon
		case schema.ChatMessagePartTypeToolSearchResult:
			if part.ToolSearchResult == nil {
				return unsupportedSessionPart(part.Type)
			}
			for _, info := range part.ToolSearchResult.Tools {
				if err := c.tool(info); err != nil {
					return err
				}
			}
		default:
			return unsupportedSessionPart(part.Type)
		}
		if common != nil {
			if err := c.metadata(common.Extra); err != nil { //nolint:staticcheck // Legacy archives still carry metadata in this deprecated field.
				return err
			}
		}
		if err := c.metadata(part.Extra); err != nil {
			return err
		}
	}
	for _, part := range m.AssistantGenMultiContent {
		c.text(part.Text)
		var common *schema.MessagePartCommon
		populated := 0
		for _, present := range []bool{part.Image != nil, part.Audio != nil, part.Video != nil, part.Reasoning != nil} {
			if present {
				populated++
			}
		}
		if part.Type == schema.ChatMessagePartTypeText && populated != 0 || part.Type != schema.ChatMessagePartTypeText && populated != 1 {
			return unsupportedSessionPart(part.Type)
		}
		switch part.Type {
		case schema.ChatMessagePartTypeText:
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(4096)
			common = &part.Image.MessagePartCommon
		case schema.ChatMessagePartTypeAudioURL:
			if part.Audio == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(8192)
			common = &part.Audio.MessagePartCommon
		case schema.ChatMessagePartTypeVideoURL:
			if part.Video == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.add(16384)
			common = &part.Video.MessagePartCommon
		case schema.ChatMessagePartTypeReasoning:
			if part.Reasoning == nil {
				return unsupportedSessionPart(part.Type)
			}
			c.text(part.Reasoning.Text)
			c.text(part.Reasoning.Signature)
		default:
			return unsupportedSessionPart(part.Type)
		}
		if common != nil {
			if err := c.metadata(common.Extra); err != nil { //nolint:staticcheck // Legacy archives still carry metadata in this deprecated field.
				return err
			}
		}
		if err := c.metadata(part.Extra); err != nil {
			return err
		}
	}
	return c.metadata(m.Extra)
}

func unsupportedSessionPart(part schema.ChatMessagePartType) error {
	return fmt.Errorf("%w: multimodal part %q", errSessionContextEstimate, part)
}

func estimateSessionTools(ctx context.Context, infos []*schema.ToolInfo) (sessionContextEstimate, error) {
	c := newSessionTokenCounter(math.MaxInt64)
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return c.sessionContextEstimate, err
		}
		if err := c.tool(info); err != nil {
			return c.sessionContextEstimate, err
		}
	}
	return c.sessionContextEstimate, nil
}

func (c *sessionTokenCounter) tool(info *schema.ToolInfo) error {
	if info == nil {
		return fmt.Errorf("%w: nil tool definition", errSessionContextEstimate)
	}
	if err := validateSessionSchemaValue(reflect.ValueOf(info.ParamsOneOf), make(map[sessionCaptureVisit]bool), 0); err != nil {
		return err
	}
	definition, err := info.ToJSONSchema()
	if err != nil {
		return fmt.Errorf("%w: tool schema", errSessionContextEstimate)
	}
	if err = validateSessionSchemaValue(reflect.ValueOf(definition), make(map[sessionCaptureVisit]bool), 0); err != nil {
		return err
	}
	if err = newSessionTokenCounter(math.MaxInt64).metadata(info.Extra); err != nil {
		return err
	}
	// Only the SDK schema codec runs here, never an arbitrary metadata marshaler.
	encoded, err := json.Marshal(struct {
		Name        string             `json:"name"`
		Description string             `json:"description"`
		Parameters  *jsonschema.Schema `json:"parameters,omitempty"`
		Extra       map[string]any     `json:"extra,omitempty"`
	}{info.Name, info.Desc, definition, info.Extra})
	if err != nil {
		return fmt.Errorf("%w: tool schema JSON", errSessionContextEstimate)
	}
	c.add(16)
	c.text(string(encoded))
	return nil
}

func validateSessionSchemaValue(v reflect.Value, active map[sessionCaptureVisit]bool, depth int) error {
	if !v.IsValid() {
		return nil
	}
	if depth > 128 {
		return fmt.Errorf("%w: schema nesting", errSessionContextEstimate)
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return validateSessionSchemaValue(v.Elem(), active, depth+1)
	}
	if v.Type() != reflect.TypeFor[*jsonschema.Schema]() && v.Type() != reflect.TypeFor[jsonschema.Schema]() &&
		v.Type() != reflect.TypeFor[*orderedmap.OrderedMap[string, *jsonschema.Schema]]() && unsupportedSessionMarshaler(v.Type()) {
		return fmt.Errorf("%w: custom schema marshaler", errSessionContextEstimate)
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Map || v.Kind() == reflect.Slice {
		if v.IsNil() {
			if v.Type() == reflect.TypeFor[*schema.ParameterInfo]() {
				return fmt.Errorf("%w: nil parameter", errSessionContextEstimate)
			}
			return nil
		}
		visit := sessionCaptureVisit{typ: v.Type(), ptr: uintptr(v.UnsafePointer())}
		if active[visit] {
			return fmt.Errorf("%w: cyclic schema", errSessionContextEstimate)
		}
		active[visit] = true
		defer delete(active, visit)
	}
	if v.Type() == reflect.TypeFor[*orderedmap.OrderedMap[string, *jsonschema.Schema]]() {
		for pair := v.Interface().(*orderedmap.OrderedMap[string, *jsonschema.Schema]).Oldest(); pair != nil; pair = pair.Next() {
			if err := validateSessionSchemaValue(reflect.ValueOf(pair.Value), active, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		return validateSessionSchemaValue(v.Elem(), active, depth+1)
	case reflect.Struct:
		if v.Type() != reflect.TypeFor[schema.ParamsOneOf]() && v.Type() != reflect.TypeFor[schema.ParameterInfo]() && v.Type() != reflect.TypeFor[jsonschema.Schema]() {
			return fmt.Errorf("%w: schema value %s", errSessionContextEstimate, v.Type())
		}
		for i := range v.NumField() {
			if v.Type() == reflect.TypeFor[schema.ParamsOneOf]() && v.Type().Field(i).Name == "jsonschema" {
				continue
			}
			field := v.Field(i)
			// Nil optional ParameterInfo pointers are normal; map entries are not.
			if field.Kind() == reflect.Pointer && field.IsNil() {
				continue
			}
			if err := validateSessionSchemaValue(field, active, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || unsupportedSessionMarshaler(v.Type().Key()) {
			return fmt.Errorf("%w: schema map key", errSessionContextEstimate)
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := validateSessionSchemaValue(iter.Value(), active, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if err := validateSessionSchemaValue(v.Index(i), active, depth+1); err != nil {
				return err
			}
		}
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	default:
		return fmt.Errorf("%w: schema value %s", errSessionContextEstimate, v.Type())
	}
	return nil
}

type sessionJSONTokenCounter struct {
	counter *sessionTokenCounter
	ascii   int64
}

func (j *sessionJSONTokenCounter) write(text string) {
	for _, r := range text {
		if j.counter.Capped {
			return
		}
		if r <= 127 {
			j.ascii++
			if j.ascii == 4 {
				j.counter.add(1)
				j.ascii = 0
			}
		} else {
			j.finish()
			j.counter.add(2)
		}
	}
}

func (j *sessionJSONTokenCounter) finish() {
	if j.ascii > 0 {
		j.counter.add(1)
		j.ascii = 0
	}
}

func (j *sessionJSONTokenCounter) quoted(text string) {
	j.write(`"`)
	for _, r := range text {
		if j.counter.Capped {
			break
		}
		switch {
		case r == '"' || r == '\\':
			j.write(`\`)
			j.write(string(r))
		case r == '\n':
			j.write(`\n`)
		case r == '\r':
			j.write(`\r`)
		case r == '\t':
			j.write(`\t`)
		case r == '\b':
			j.write(`\b`)
		case r == '\f':
			j.write(`\f`)
		case r < 32 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
			j.write(`\u`)
			j.write(fmt.Sprintf("%04x", r))
		default:
			j.write(string(r))
		}
	}
	j.write(`"`)
}

func (c *sessionTokenCounter) metadata(extra map[string]any) error {
	if len(extra) == 0 {
		return nil
	}
	j := sessionJSONTokenCounter{counter: c}
	err := j.value(reflect.ValueOf(extra), make(map[sessionCaptureVisit]bool), 0)
	j.finish()
	return err
}

func (j *sessionJSONTokenCounter) value(v reflect.Value, active map[sessionCaptureVisit]bool, depth int) error {
	if !v.IsValid() {
		j.write("null")
		return nil
	}
	if depth > 128 {
		return fmt.Errorf("%w: metadata nesting", errSessionContextEstimate)
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			j.write("null")
			return nil
		}
		return j.value(v.Elem(), active, depth+1)
	}
	if v.Type() == reflect.TypeFor[json.Number]() {
		number := v.String()
		if !json.Valid([]byte(number)) || number == "" || number[0] != '-' && (number[0] < '0' || number[0] > '9') {
			return fmt.Errorf("%w: JSON number", errSessionContextEstimate)
		}
		j.write(number)
		return nil
	}
	if unsupportedSessionMarshaler(v.Type()) {
		return fmt.Errorf("%w: custom metadata marshaler", errSessionContextEstimate)
	}
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		return fmt.Errorf("%w: dynamic raw JSON", errSessionContextEstimate)
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Map || v.Kind() == reflect.Slice {
		if v.IsNil() {
			j.write("null")
			return nil
		}
		visit := sessionCaptureVisit{typ: v.Type(), ptr: uintptr(v.UnsafePointer())}
		if active[visit] {
			return fmt.Errorf("%w: cyclic metadata", errSessionContextEstimate)
		}
		active[visit] = true
		defer delete(active, visit)
	}
	switch v.Kind() {
	case reflect.Pointer:
		return j.value(v.Elem(), active, depth+1)
	case reflect.String:
		j.quoted(v.String())
	case reflect.Bool:
		j.write(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		j.write(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		j.write(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0) {
			return fmt.Errorf("%w: non-finite metadata", errSessionContextEstimate)
		}
		j.write(strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits()))
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || unsupportedSessionMarshaler(v.Type().Key()) {
			return fmt.Errorf("%w: metadata map key", errSessionContextEstimate)
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, k int) bool { return keys[i].String() < keys[k].String() })
		j.write("{")
		for i, key := range keys {
			if i > 0 {
				j.write(",")
			}
			j.quoted(key.String())
			j.write(":")
			if err := j.value(v.MapIndex(key), active, depth+1); err != nil {
				return err
			}
		}
		j.write("}")
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			// JSON's []byte representation is visible base64 text, not a media part.
			j.write(`"`)
			n := int64(v.Len())
			j.counter.add(n / 3)
			if n%3 != 0 {
				j.counter.add(1)
			}
			j.write(`"`)
			return nil
		}
		j.write("[")
		for i := range v.Len() {
			if i > 0 {
				j.write(",")
			}
			if err := j.value(v.Index(i), active, depth+1); err != nil {
				return err
			}
		}
		j.write("]")
	default:
		return fmt.Errorf("%w: metadata value %s", errSessionContextEstimate, v.Type())
	}
	return nil
}

func unsupportedSessionMarshaler(t reflect.Type) bool {
	return t.Implements(reflect.TypeFor[json.Marshaler]()) || t.Implements(reflect.TypeFor[encoding.TextMarshaler]()) ||
		reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[encoding.TextMarshaler]())
}
