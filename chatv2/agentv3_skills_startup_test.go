//go:build !386 && !arm

package chatv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAgentV3StartupSkillSnapshotsMakesZeroOrOneRuntimeCall(t *testing.T) {
	snapshot := mustAgentV3RuntimeSnapshot(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "Bearer runtime-secret", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(snapshot))
	}))
	defer server.Close()
	client := &RemoteRuntimeClient{Endpoint: server.URL, AuthToken: "runtime-secret", HTTPClient: server.Client()}

	withoutRuntime, err := loadAgentV3StartupSkillSnapshots(t.Context(), &config.AgentV3Config{}, client)
	require.NoError(t, err)
	assert.Equal(t, emptyAgentV3SkillSnapshot(agentV3SkillSourceBotLocal), withoutRuntime.BotLocal)
	assert.Equal(t, emptyAgentV3SkillSnapshot(agentV3SkillSourceRuntimeGlobal), withoutRuntime.RuntimeGlobal)
	assert.Zero(t, calls.Load())

	withRuntime, err := loadAgentV3StartupSkillSnapshots(t.Context(), &config.AgentV3Config{
		Skills: config.AgentV3SkillsConfig{RuntimeGlobal: true},
	}, client)
	require.NoError(t, err)
	assert.Equal(t, snapshot, withRuntime.RuntimeGlobal)
	assert.Equal(t, int32(1), calls.Load())

	chats := []*CompiledChat{
		{AgentV3StartupSkills: withRuntime},
		{AgentV3StartupSkills: withRuntime},
	}
	for _, chat := range chats {
		sources := []agentV3SkillSnapshot{
			chat.AgentV3StartupSkills.BotLocal,
			chat.AgentV3StartupSkills.RuntimeGlobal,
		}
		assert.Equal(t, snapshot, sources[1])
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestLoadAgentV3StartupSkillSnapshotsFreezesBotLocalContent(t *testing.T) {
	root := t.TempDir()
	writeAgentV3SkillFile(t, root, "local-skill", "# Local skill\nInitial description.\n")

	startup, err := loadAgentV3StartupSkillSnapshots(t.Context(), &config.AgentV3Config{
		Skills: config.AgentV3SkillsConfig{Root: root},
	}, nil)
	require.NoError(t, err)
	require.Len(t, startup.BotLocal.Skills, 1)

	writeAgentV3SkillFile(t, root, "local-skill", "# Local skill\nChanged description.\n")
	assert.Equal(t, "Initial description.", startup.BotLocal.Skills[0].Description)
	assert.Equal(t, "# Local skill\nInitial description.\n", startup.BotLocal.Skills[0].Content)
}

func writeAgentV3SkillFile(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}
