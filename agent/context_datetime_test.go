package agentv3

import (
	"encoding/xml"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestContextDateTimeIdentifiesAbsoluteInstant(t *testing.T) {
	instant := time.Date(2026, time.October, 16, 20, 36, 53, 0, time.UTC)
	tc := &TurnContext{
		Message: &tb.Message{Text: "hello"},
		V3:      &AgentV3TurnState{frameTime: instant.In(beijingFallbackLocation)},
	}
	cc := &CompiledAgent{}
	builders := map[string]func() (*schema.Message, error){
		"reply session": func() (*schema.Message, error) {
			return buildReplySessionPromptAddition(cc, tc)
		},
		"chat": func() (*schema.Message, error) {
			return buildAgentV3UserMessage(cc, tc, nil, nil)
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			msg, err := build()
			require.NoError(t, err)
			var rendered struct {
				DateTime struct {
					Instant string `xml:"rfc3339,attr"`
					Zone    string `xml:"timezone,attr"`
					Text    string `xml:",chardata"`
				} `xml:"datetime"`
			}
			require.NoError(t, xml.Unmarshal([]byte(msg.Content), &rendered))
			parsed, err := time.Parse(time.RFC3339, rendered.DateTime.Instant)
			require.NoError(t, err)
			require.True(t, instant.Equal(parsed))
			require.Equal(t, "Asia/Shanghai", rendered.DateTime.Zone)
			local, err := time.ParseInLocation("2006-01-02 15:04:05", rendered.DateTime.Text, beijingFallbackLocation)
			require.NoError(t, err)
			require.True(t, instant.Equal(local))
		})
	}
}
