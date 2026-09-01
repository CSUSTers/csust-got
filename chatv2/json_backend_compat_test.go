//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"encoding/json"
	"runtime/debug"
	"testing"

	jsonschema "github.com/eino-contrib/jsonschema"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type jsonBackendPayload struct {
	Message    schema.Message    `json:"message"`
	ToolInfo   schema.ToolInfo   `json:"tool_info"`
	JSONSchema jsonschema.Schema `json:"json_schema"`
	Integer    int64             `json:"integer"`
	Float      float64           `json:"float"`
	Number     json.Number       `json:"number"`
	Nullable   any               `json:"nullable"`
	Metadata   map[string]any    `json:"metadata"`
}

type jsonBackendCustomMarshaler struct {
	Text string
}

func (value jsonBackendCustomMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"text":   value.Text,
		"source": "custom-marshaler",
	})
}

func representativeJSONBackendPayload(t testing.TB) jsonBackendPayload {
	t.Helper()

	var parameters jsonschema.Schema
	require.NoError(t, json.Unmarshal([]byte(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"query": {"type": "string", "description": "Search text"},
			"limit": {"type": "integer", "minimum": 1}
		},
		"required": ["query"]
	}`), &parameters))

	index := 0
	return jsonBackendPayload{
		Message: schema.Message{
			Role:    schema.Assistant,
			Content: "你好，世界 🛰️ \"quoted\" \\ slash\nnext\tcolumn <tag>&",
			ToolCalls: []schema.ToolCall{
				{
					Index: &index,
					ID:    "call_weather_1",
					Type:  "function",
					Function: schema.FunctionCall{
						Name:      "weather_lookup",
						Arguments: `{"query":"长沙 ☔","limit":2,"escaped":"quote \" and slash \\"}`,
					},
					Extra: map[string]any{
						"provider": "paimon",
						"attempt":  int64(2),
					},
				},
			},
		},
		ToolInfo: schema.ToolInfo{
			Name:        "weather_lookup",
			Desc:        "Look up weather with Unicode ☔ and escaped \"text\".",
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameters),
			Extra: map[string]any{
				"service": "weather",
			},
		},
		JSONSchema: parameters,
		Integer:    922337203685477580,
		Float:      3.1415926,
		Number:     json.Number("1234567890.123456789"),
		Nullable:   nil,
		Metadata: map[string]any{
			"nested": map[string]any{
				"slice": []any{
					"汉字",
					int64(7),
					float64(-0.25),
					json.Number("42"),
					nil,
					map[string]any{"escaped": "line\nquote\"backslash\\"},
				},
			},
		},
	}
}

func assertJSONSemanticallyEqual(t testing.TB, want, got []byte) {
	t.Helper()

	wantDecoder := json.NewDecoder(bytes.NewReader(want))
	wantDecoder.UseNumber()
	var wantValue any
	require.NoError(t, wantDecoder.Decode(&wantValue))

	gotDecoder := json.NewDecoder(bytes.NewReader(got))
	gotDecoder.UseNumber()
	var gotValue any
	require.NoError(t, gotDecoder.Decode(&gotValue))
	require.Equal(t, wantValue, gotValue)
}

func containsToolJSONSchema(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if properties, ok := value["properties"].(map[string]any); ok {
			if _, ok := properties["query"]; ok {
				return true
			}
		}
		for _, child := range value {
			if containsToolJSONSchema(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsToolJSONSchema(child) {
				return true
			}
		}
	}

	return false
}

func TestJSONBackendReplacementIdentity(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	require.True(t, ok)

	var sonicModule *debug.Module
	for _, dependency := range info.Deps {
		if dependency.Path == "github.com/bytedance/sonic" {
			sonicModule = dependency
			break
		}
	}

	require.NotNil(t, sonicModule)
	require.NotNil(t, sonicModule.Replace)
	require.Equal(t, "github.com/hugefiver/paimon-go", sonicModule.Replace.Path)
	require.Equal(t, "v0.1.0", sonicModule.Replace.Version)
}

func TestJSONBackendEinoPayloadParity(t *testing.T) {
	payload := representativeJSONBackendPayload(t)

	sonicEncoded, err := sonic.Marshal(payload)
	require.NoError(t, err)
	standardEncoded, err := json.Marshal(payload)
	require.NoError(t, err)
	assertJSONSemanticallyEqual(t, standardEncoded, sonicEncoded)

	var roundTripped jsonBackendPayload
	require.NoError(t, sonic.Unmarshal(sonicEncoded, &roundTripped))
	require.Equal(t, payload.Integer, roundTripped.Integer)
	require.Equal(t, payload.Float, roundTripped.Float)
	require.Equal(t, payload.Number, roundTripped.Number)
	require.Equal(t, payload.Nullable, roundTripped.Nullable)
	roundTrippedEncoded, err := sonic.Marshal(roundTripped)
	require.NoError(t, err)
	assertJSONSemanticallyEqual(t, sonicEncoded, roundTrippedEncoded)

	var decoded map[string]any
	require.NoError(t, sonic.Unmarshal(sonicEncoded, &decoded))
	message := decoded["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	function := toolCalls[0].(map[string]any)["function"].(map[string]any)
	require.Equal(t, "weather_lookup", function["name"])
	require.Equal(t, payload.Message.ToolCalls[0].Function.Arguments, function["arguments"])
	require.True(t, containsToolJSONSchema(decoded["json_schema"]))
	require.Equal(t, nil, decoded["nullable"])
	require.Equal(t, float64(payload.Integer), decoded["integer"])
	require.Equal(t, payload.Float, decoded["float"])
	require.Equal(t, float64(1234567890.123456789), decoded["number"])
}

func TestJSONBackendStringAndCustomMarshalerParity(t *testing.T) {
	value := jsonBackendCustomMarshaler{Text: "自定义 \"marshal\" \\ value"}

	sonicEncoded, err := sonic.MarshalString(value)
	require.NoError(t, err)
	standardEncoded, err := json.Marshal(value)
	require.NoError(t, err)
	assertJSONSemanticallyEqual(t, standardEncoded, []byte(sonicEncoded))

	var decoded map[string]string
	require.NoError(t, sonic.UnmarshalString(sonicEncoded, &decoded))
	require.Equal(t, map[string]string{
		"text":   value.Text,
		"source": "custom-marshaler",
	}, decoded)
}

func TestJSONBackendASTParity(t *testing.T) {
	payload := representativeJSONBackendPayload(t)
	encoded, err := sonic.Marshal(payload)
	require.NoError(t, err)

	node, err := sonic.GetFromString(string(encoded), "message", "tool_calls", 0, "function", "arguments")
	require.NoError(t, err)
	arguments, err := node.MarshalJSON()
	require.NoError(t, err)
	target, err := json.Marshal(payload.Message.ToolCalls[0].Function.Arguments)
	require.NoError(t, err)
	assertJSONSemanticallyEqual(t, target, arguments)
}

func TestJSONBackendValidAndErrorBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "nested Unicode escaped number null",
			input: `{"nested":[{"unicode":"你好 \u2603","escaped":"quote \" slash \\ newline \n","number":-12.5e+3,"null":null}]}`,
		},
		{name: "top-level number", input: `-12.5e+3`},
		{name: "top-level null", input: `null`},
		{name: "malformed object", input: `{"broken":}`},
		{name: "trailing value", input: `{} {}`},
		{name: "trailing token", input: `{}x`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.input)
			require.Equal(t, json.Valid(input), sonic.Valid(input))

			var standardValue any
			standardErr := json.Unmarshal(input, &standardValue)
			var sonicValue any
			sonicErr := sonic.Unmarshal(input, &sonicValue)
			require.Equal(t, standardErr == nil, sonicErr == nil)
			if standardErr == nil {
				require.Equal(t, standardValue, sonicValue)
			}
		})
	}
}
