package base

import (
	"csust-got/config"
	"csust-got/log"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVoiceMeta(t *testing.T) {
	t.Skip("Need Meilisearch service for test")
	t.Parallel()

	config.InitViper("../config.yaml", "BOT")
	config.BotConfig = config.NewBotConfig()
	config.ReadConfig(
		config.BotConfig.GetVoiceConfig)
	config.BotConfig.DebugMode = true
	log.InitLogger()
	InitGetVoice()

	index := "genshin"

	t.Run("index not found", func(t *testing.T) {
		meta, err := getVoiceMeta("123", &GetVoiceQuery{})
		assert.Nil(t, meta, "meta should be nil")
		assert.ErrorIs(t, err, ErrIndexNotFound)
	})

	t.Run("character not exist", func(t *testing.T) {
		meta, err := getVoiceMeta(index, &GetVoiceQuery{Character: "123"})
		assert.ErrorIs(t, err, ErrNoAudioFound)
		assert.Nil(t, meta, "meta should be nil")
	})

	t.Run("random audio", func(t *testing.T) {
		meta, err := getVoiceMeta(index, &GetVoiceQuery{})
		assert.NoError(t, err)
		assert.NotNil(t, meta, "meta should not be nil")
		t.Log(meta)
	})

	t.Run("random audio of character", func(t *testing.T) {
		ch := "派蒙"
		meta, err := getVoiceMeta(index, &GetVoiceQuery{Character: ch})
		assert.NoError(t, err)
		assert.NotNil(t, meta, "meta should not be nil")
		assert.Equal(t, ch, meta.Ch, "character should be the same")
		t.Log(meta)
	})

	t.Run("search audio", func(t *testing.T) {
		meta, err := getVoiceMeta(index, &GetVoiceQuery{Text: "修都修了"})
		assert.NoError(t, err)
		assert.NotNil(t, meta, "meta should not be nil")
		assert.Equal(t, "修都修了，就别说这种话啦…", meta.Text, "text should be the same")
		t.Log(meta)
	})
}
