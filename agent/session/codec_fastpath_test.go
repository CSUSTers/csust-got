package session

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestCodecCanonicalFastPathFixturesDetached(t *testing.T) {
	for name, fixture := range codecBenchmarkFixtures() {
		t.Run(name, func(t *testing.T) {
			original, err := json.Marshal(fixture)
			require.NoError(t, err)
			out, err := Snapshot(fixture)
			require.NoError(t, err)
			canonical, err := json.Marshal(out)
			require.NoError(t, err)
			require.True(t, bytes.Equal(original, canonical), "fixture exercises byte-equal fast path")
			out.Delta[0].Message.Content = "mutated snapshot"
			require.NotEqual(t, out.Delta[0].Message.Content, fixture.Delta[0].Message.Content)
			if name == "media" {
				*out.Delta[0].Message.UserInputMultiContent[0].Image.Base64Data = "mutated"
				require.NotEqual(t, "mutated", *fixture.Delta[0].Message.UserInputMultiContent[0].Image.Base64Data)
			}
			if name == "params" || name == "jsonschema" {
				tool := out.Delta[0].Message.UserInputMultiContent[0].ToolSearchResult.Tools[0]
				tool.Extra["integer"] = 99
				*tool.ParamsOneOf = *schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})
				still, err := json.Marshal(fixture)
				require.NoError(t, err)
				require.Equal(t, original, still)
			}
		})
	}
}

type codecReorderedJSON struct{}

func (codecReorderedJSON) MarshalJSON() ([]byte, error) { return []byte(`{"z":1,"a":2}`), nil }

type codecLossyJSON struct{}

func (codecLossyJSON) MarshalJSON() ([]byte, error) {
	return []byte(`{"number":9007199254740993}`), nil
}

func codecUnequalCapture() TurnCapture {
	return TurnCapture{Delta: History(&schema.Message{Role: schema.User, Content: "input", Extra: map[string]any{"reordered": codecReorderedJSON{}, "number": int64(9007199254740993)}}, schema.AssistantMessage("answer", nil)), Complete: true}
}

func TestCodecUnequalCanonicalKeepsLossCheck(t *testing.T) {
	c := codecUnequalCapture()
	original, err := json.Marshal(c)
	require.NoError(t, err)
	out, err := Snapshot(c)
	require.NoError(t, err)
	canonical, err := json.Marshal(out)
	require.NoError(t, err)
	require.False(t, bytes.Equal(original, canonical), "ordering takes the original comparison fallback")
	require.Equal(t, json.Number("9007199254740993"), out.Delta[0].Message.Extra["number"])
	c.Delta[0].Message.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{{Name: "lossy", Extra: map[string]any{"loss": codecLossyJSON{}}}}}}}
	_, err = Snapshot(c)
	require.ErrorIs(t, err, ErrCorrupt, "SDK metadata precision loss must still be rejected")
}

func BenchmarkSessionCodecUnequalCanonical(b *testing.B) {
	fixture := codecUnequalCapture()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Snapshot(fixture); err != nil {
			b.Fatal(err)
		}
	}
}
