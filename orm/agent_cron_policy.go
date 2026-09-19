package orm

import (
	"context"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// AgentCronPolicyState contains the permission state relevant to a cron run.
type AgentCronPolicyState struct {
	WhiteList []string
	BlockList []string
	Shutdown  bool
	Banned    bool
}

// AgentCronPolicy loads the current permission state for a chat member.
func AgentCronPolicy(ctx context.Context, chatID, userID int64) (AgentCronPolicyState, error) {
	var state AgentCronPolicyState
	pipe := rc.Pipeline()
	whiteCmd := pipe.SMembers(ctx, wrapKey("white_list"))
	blockCmd := pipe.SMembers(ctx, wrapKey("black_list"))
	shutdownCmd := pipe.Get(ctx, wrapKeyWithChat("shutdown", chatID))
	bannedCmd := pipe.Get(ctx, wrapKeyWithChatMember("banned", chatID, userID))
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return state, err
	}
	if err := whiteCmd.Err(); err != nil {
		return state, err
	}
	if err := blockCmd.Err(); err != nil {
		return state, err
	}
	shutdown, err := agentCronPolicyBool(shutdownCmd)
	if err != nil {
		return state, err
	}
	banned, err := agentCronPolicyBool(bannedCmd)
	if err != nil {
		return state, err
	}
	state.WhiteList = whiteCmd.Val()
	state.BlockList = blockCmd.Val()
	state.Shutdown = shutdown
	state.Banned = banned
	return state, nil
}

func agentCronPolicyBool(command *redis.StringCmd) (bool, error) {
	value, err := command.Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return false, err
	}
	return parsed > 0, nil
}
