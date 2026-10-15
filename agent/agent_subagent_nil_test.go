package agentv3

import (
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/require"
)

func TestBuildConfiguredAgentToolsSkipsNullSubAgent(t *testing.T) {
	require.NotPanics(t, func() {
		got, err := buildConfiguredAgentTools(t.Context(), "v3", &config.AgentOptions{SubAgents: []*config.SubAgentConfig{nil}}, nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})
}
