package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAgentV3SessionCompactConfig(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)
	tests := []struct {
		name          string
		raw           string
		valid         bool
		wantEnable    bool
		wantThreshold int64
		wantKeep      int
		wantMaxChars  int
		wantModel     string
	}{
		{name: "omitted", raw: "", valid: true, wantThreshold: 120000, wantKeep: 2, wantMaxChars: 6000},
		{name: "empty object", raw: "compact: {}", valid: true, wantThreshold: 120000, wantKeep: 2, wantMaxChars: 6000},
		{name: "explicit", raw: "compact: {enable: true, threshold_tokens: 50000, keep_recent_turns: 3, summary_max_chars: 2000, model: {name: cheap, model: gpt-4o-mini, base_url: 'https://api.example.com/v1', api_key: k, retry_nums: 2}}", valid: true, wantEnable: true, wantThreshold: 50000, wantKeep: 3, wantMaxChars: 2000, wantModel: "gpt-4o-mini"},
		{name: "string numbers", raw: "compact: {enable: 'true', threshold_tokens: '7', keep_recent_turns: '1', summary_max_chars: '9'}", valid: true, wantEnable: true, wantThreshold: 7, wantKeep: 1, wantMaxChars: 9},
		{name: "zero threshold", raw: "compact: {threshold_tokens: 0}", valid: false},
		{name: "negative keep", raw: "compact: {keep_recent_turns: -1}", valid: false},
		{name: "fractional chars", raw: "compact: {summary_max_chars: 1.5}", valid: false},
		{name: "non-boolean enable", raw: "compact: {enable: yes}", valid: false},
		{name: "model without name", raw: "compact: {model: {name: cheap}}", valid: false},
		{name: "model as string", raw: "compact: {model: gpt-4o-mini}", valid: false},
		{name: "model with wrong type", raw: "compact: {model: {model: x, retry_nums: many}}", valid: false},
		{name: "scalar compact", raw: "compact: true", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agent_v3:\n  session:\n    "+tt.raw)))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			err := cfg.Session.Validate()
			if !tt.valid {
				require.ErrorIs(t, err, errInvalidAgentV3SessionCompact)
				return
			}
			require.NoError(t, err)
			compact := cfg.Session.Compact
			require.Equal(t, tt.wantEnable, compact.Enable)
			require.Equal(t, tt.wantThreshold, compact.Threshold(cfg.Session.ContextOverflow.TokenLimit()))
			require.Equal(t, tt.wantKeep, compact.RecentTurns())
			require.Equal(t, tt.wantMaxChars, compact.MaxSummaryChars())
			if tt.wantModel == "" {
				require.Nil(t, compact.Model)
			} else {
				require.NotNil(t, compact.Model)
				require.Equal(t, tt.wantModel, compact.Model.Model)
				require.Equal(t, 2, compact.Model.RetryNums)
			}
			require.EqualValues(t, 200000, cfg.Session.ContextOverflow.TokenLimit(), "compact does not disturb the overflow limit")
		})
	}
}

func TestAgentV3SessionCompactThresholdDefaultsToSixtyPercent(t *testing.T) {
	var compact AgentV3SessionCompactConfig
	require.EqualValues(t, 19660, compact.Threshold(32768))
	require.EqualValues(t, 1, compact.Threshold(1))
	compact.ThresholdTokens = 5
	require.EqualValues(t, 5, compact.Threshold(32768))
}
