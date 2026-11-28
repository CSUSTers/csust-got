package base

import (
	"slices"
	"testing"

	"csust-got/config"
	"csust-got/log"

	"github.com/stretchr/testify/require"
)

func TestDecodeTextUTF16BEAndCodeBlockEscaping(t *testing.T) {
	origConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = origConfig })
	config.BotConfig = config.NewBotConfig()
	log.InitLogger()

	encoded, err := decodeText("utf8", "utf16be", "你好ABCD")
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := decodeText("utf16be", "utf8", encoded)
	require.NoError(t, err)
	require.Equal(t, "你好ABCD", decoded)

	hexEncoded, err := decodeText("utf16be", "hex", "Hi")
	require.NoError(t, err)
	require.Equal(t, "00480069", hexEncoded)

	fromHex, err := decodeText("hex", "utf16be", hexEncoded)
	require.NoError(t, err)
	require.Equal(t, "Hi", fromHex)

	require.Equal(t, `a_b-c.d*e\\f\`+"`"+`g`, escapeMdCodeBlockChars("a_b-c.d*e\\f`g"))
}

func TestSearchEngineAndHitokotoHTMLEscaping(t *testing.T) {
	raw := `<script>&"test"`
	require.Contains(t, google(raw), `&lt;script&gt;&amp;&#34;test&#34;</a>`)
	require.Contains(t, bing(raw), `&lt;script&gt;&amp;&#34;test&#34;</a>`)
	require.Contains(t, bilibili(raw), `&lt;script&gt;&amp;&#34;test&#34;</a>`)
	require.Contains(t, github(raw), `&lt;script&gt;&amp;&#34;test&#34;</a>`)
	require.Equal(t, `&lt;script&gt;&amp;&#34;test&#34;`, repeat(raw))

	resp := HitokotoResponse{
		Sentence: "a < b & c",
		Author:   "x<y>",
		From:     "src&1",
	}
	require.Equal(t, "a < b & c", resp.format(false))

	resp2 := HitokotoResponse{
		Sentence: "a < b & c",
		Author:   "x<y>",
		From:     "src&1",
	}
	require.Equal(t, "a &lt; b &amp; c by <em>x&lt;y&gt; 《src&amp;1》</em>", resp2.format(true))
}

func TestGetVoiceMetaVoiceDbAndFilterEscaping(t *testing.T) {
	origAlias := getVoiceAlias
	t.Cleanup(func() { getVoiceAlias = origAlias })

	emptyDb := NewVoiceDb(slices.Values([]*VoiceChip{}))
	populatedDb := NewVoiceDb(slices.Values([]*VoiceChip{
		{Ch: "alice", Text: "hi", FullPath: "audio/hi.ogg"},
	}))

	getVoiceAlias = map[string]*VoiceConfigNDb{
		"empty": {
			IndexConfig: &config.IndexConfig{Name: "Empty", VoiceBaseUrl: "https://cdn.example.com/"},
			VoiceDb:     emptyDb,
		},
		"ok": {
			IndexConfig: &config.IndexConfig{Name: "Game", Alias: []string{"ok"}, VoiceBaseUrl: "https://cdn.example.com/voices"},
			VoiceDb:     populatedDb,
		},
	}

	_, err := getVoiceMeta("empty", &GetVoiceQuery{})
	require.ErrorIs(t, err, ErrNoAudioFound)

	_, err = getVoiceMeta("ok", &GetVoiceQuery{Character: "missing"})
	require.ErrorIs(t, err, ErrNoAudioFound)

	meta, err := getVoiceMeta("ok", &GetVoiceQuery{Character: "alice"})
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/voices/audio/hi.ogg", meta.Url)

	_, _, err = getVoiceMetaFromIndexes([]*VoiceConfigNDb{
		{IndexConfig: &config.IndexConfig{Name: "NoAlias", IndexUid: "uid1"}},
	}, &GetVoiceQuery{Text: "hello"}, false)
	require.ErrorIs(t, err, ErrNoAudioFound)

	require.Equal(t, `ch = 'a\'b\\c'`, buildVoiceCharacterFilter(`a'b\c`))
	require.Equal(t, `ch STARTS WITH 'a\'b'`, buildVoiceCharacterFilter(`a'b*`))
	require.Equal(t, `ch CONTAINS 'a\'b'`, buildVoiceCharacterFilter(`*a'b`))
}

func TestComputeSasEmptyAndBounds(t *testing.T) {
	origConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = origConfig })

	config.BotConfig = config.NewBotConfig()
	config.BotConfig.McConfig.Sacrifices = nil
	require.Equal(t, 0, computeSas(0))

	config.BotConfig.McConfig.Sacrifices = []int{10, 20}
	require.Equal(t, 10, computeSas(-1))
	require.Equal(t, 10, computeSas(0))
	require.Equal(t, 20, computeSas(5))
}
