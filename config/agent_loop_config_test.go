package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestModelLoopTimeoutGetters(t *testing.T) {
	tests := []struct {
		name     string
		model    *Model
		wantReq  time.Duration
		wantIdle time.Duration
	}{
		{"nil model", nil, 0, 60 * time.Second},
		{"defaults", &Model{}, 0, 60 * time.Second},
		{"configured", &Model{RequestTimeout: "5m", StreamIdleTimeout: "45s"}, 5 * time.Minute, 45 * time.Second},
		{"zero disables idle watchdog", &Model{StreamIdleTimeout: "0s"}, 0, 0},
		{"negative falls back", &Model{RequestTimeout: "-1s", StreamIdleTimeout: "-5s"}, 0, 60 * time.Second},
		{"garbage falls back", &Model{RequestTimeout: "soon", StreamIdleTimeout: "later"}, 0, 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantReq, tt.model.RequestTimeoutDuration())
			require.Equal(t, tt.wantIdle, tt.model.StreamIdleTimeoutDuration())
		})
	}
}

func TestAgentOptionsFinalReserve(t *testing.T) {
	tests := []struct {
		name string
		opts *AgentOptions
		want time.Duration
	}{
		{"nil", nil, 90 * time.Second},
		{"default", &AgentOptions{}, 90 * time.Second},
		{"configured", &AgentOptions{FinalReserve: "2m"}, 2 * time.Minute},
		{"zero disables", &AgentOptions{FinalReserve: "0s"}, 0},
		{"negative falls back", &AgentOptions{FinalReserve: "-3s"}, 90 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.opts.GetFinalReserve())
		})
	}
}

func TestAgentV3ConcurrencyConfigUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     AgentV3ConcurrencyConfig
		wantBusy string
	}{
		{"omitted", "agent_v3:\n  enable: true\n", AgentV3ConcurrencyConfig{}, "当前任务太多，稍后再试。"},
		{"configured", "agent_v3:\n  concurrency:\n    max_runs: 4\n    max_model_calls: 2\n    busy_message: ' queue is full '\n", AgentV3ConcurrencyConfig{MaxRuns: 4, MaxModelCalls: 2, BusyMessage: " queue is full "}, "queue is full"},
		{"blank message uses default", "agent_v3:\n  concurrency:\n    max_runs: 1\n    busy_message: '   '\n", AgentV3ConcurrencyConfig{MaxRuns: 1, BusyMessage: "   "}, "当前任务太多，稍后再试。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader(tt.raw)))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			require.Equal(t, tt.want, cfg.Concurrency)
			require.Equal(t, tt.wantBusy, cfg.Concurrency.GetBusyMessage())
		})
	}
}

func TestModelAndAgentLoopKeysUnmarshal(t *testing.T) {
	raw := "agents:\n  - name: a\n    model:\n      name: m\n      request_timeout: 3m\n      stream_idle_timeout: 20s\n    agent:\n      enable: true\n      max_steps: 8\n      final_reserve: 45s\n"
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(raw)))
	var agents AgentV3Configs
	require.NoError(t, v.UnmarshalKey("agents", &agents, viper.DecodeHook(DispatchFor())))
	require.Len(t, agents, 1)
	require.Equal(t, 3*time.Minute, agents[0].Model.RequestTimeoutDuration())
	require.Equal(t, 20*time.Second, agents[0].Model.StreamIdleTimeoutDuration())
	require.Equal(t, 45*time.Second, agents[0].Agent.GetFinalReserve())
}
