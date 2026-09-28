package orm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentCronPolicyReadsCurrentRedisStateAndReturnsErrors(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	chatID := int64(-2100)
	userID := int64(121)
	require.NoError(t, rc.SAdd(ctx, wrapKey("white_list"), "-2100", "-2101").Err())
	require.NoError(t, rc.SAdd(ctx, wrapKey("black_list"), "-2200").Err())
	require.NoError(t, rc.Set(ctx, wrapKeyWithChat("shutdown", chatID), 1, 0).Err())
	require.NoError(t, rc.Set(ctx, wrapKeyWithChatMember("banned", chatID, userID), 1, 0).Err())

	state, err := AgentCronPolicy(ctx, chatID, userID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"-2100", "-2101"}, state.WhiteList)
	assert.Equal(t, []string{"-2200"}, state.BlockList)
	assert.True(t, state.Shutdown)
	assert.True(t, state.Banned)

	require.NoError(t, rc.Close())
	_, err = AgentCronPolicy(ctx, chatID, userID)
	assert.Error(t, err)
}
