package config

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentV3ConfigsReadConfigAutoEnablesMissingOptions(t *testing.T) {
	const config = `
models:
  - &gpt
    name: gpt-3.5
    base_url: "http://test.com"
    api_key: &key "test-key"
  - &qwen
    name: qwen3:32b
    base_url: "http://test.com"
    api_key: *key
agents:
  - &c1 
    name: test
    message_context: 5
    system_prompt: "test prompt"
    model: *gpt
  - <<: *c1
    system_prompt:
    - "line 1\n"
    - |
      line 2
    - 'line3
	  line3'
    - "
	  line3"
    - >+

      line4
      line4

    - |-
      line6
      line7
`
	viper.SetConfigType("yaml")
	assert.NoError(t, viper.ReadConfig(strings.NewReader(config)))

	var c AgentV3Configs
	c.readConfig()
	assert.Len(t, c, 2)
	assert.True(t, c[0].Agent.Enable)
	assert.True(t, c[1].Agent.Enable)
	assert.Equal(t, "test prompt", c[0].SystemPrompt.String())
	assert.Equal(t, "line 1\nline 2\nline3 line3 line3\nline4 line4\n\nline6\nline7", c[1].SystemPrompt.String())
}

func TestAgentGetMaxSteps(t *testing.T) {
	t.Run("tool-enabled main agent clamps too-low max steps", func(t *testing.T) {
		cfg := &AgentOptions{
			MaxSteps: 1,
			Tools:    []string{"update_progress"},
		}
		assert.Equal(t, minToolAgentMaxSteps, cfg.GetMaxSteps())
	})

	t.Run("tool-enabled subagent clamps too-low max steps", func(t *testing.T) {
		cfg := &SubAgentConfig{
			MaxSteps: 2,
			Tools:    []string{"get_context"},
		}
		assert.Equal(t, minToolAgentMaxSteps, cfg.GetMaxSteps())
	})

	t.Run("tool-free agent preserves explicit low max steps", func(t *testing.T) {
		cfg := &AgentOptions{MaxSteps: 1}
		assert.Equal(t, 1, cfg.GetMaxSteps())
	})

	t.Run("default values stay unchanged when max steps unset", func(t *testing.T) {
		assert.Equal(t, defaultAgentMaxSteps, (&AgentOptions{}).GetMaxSteps())
		assert.Equal(t, defaultSubAgentMaxSteps, (&SubAgentConfig{}).GetMaxSteps())
	})
}

func TestModelRetryDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults to three retries with 500ms initial delay", func(t *testing.T) {
		var cfg *Model
		assert.Equal(t, 3, cfg.RetryCount())
		assert.Equal(t, 500*time.Millisecond, cfg.RetryInitialDelay())
	})

	t.Run("explicit retry count and duration string are respected", func(t *testing.T) {
		cfg := &Model{
			RetryNums:            5,
			RetryInitialInterval: "750ms",
		}
		assert.Equal(t, 5, cfg.RetryCount())
		assert.Equal(t, 750*time.Millisecond, cfg.RetryInitialDelay())
	})

	t.Run("legacy retry interval remains seconds", func(t *testing.T) {
		cfg := &Model{RetryInterval: 2}
		assert.Equal(t, 2*time.Second, cfg.RetryInitialDelay())
	})
}

func TestAgentRichConfigParses(t *testing.T) {
	const raw = `
agents:
  - name: rich-agent
    agent:
      enable: true
      rich: true
`
	v := viper.New()
	v.SetConfigType("yaml")
	assert.NoError(t, v.ReadConfig(strings.NewReader(raw)))

	var cfg AgentV3Configs
	assert.NoError(t, v.UnmarshalKey("agents", &cfg, viper.DecodeHook(DispatchFor())))

	if assert.Len(t, cfg, 1) && assert.NotNil(t, cfg[0].Agent) {
		assert.True(t, cfg[0].Agent.Rich)
	}
}

func TestIsAgentV3RichEnabledRequiresEnabledAgentAndRichGate(t *testing.T) {
	old := BotConfig
	t.Cleanup(func() { BotConfig = old })
	BotConfig = &Config{AgentV3: &AgentV3Config{Enable: true}}

	tests := []struct {
		name string
		cfg  *AgentConfig
		want bool
	}{
		{name: "nil config is false"},
		{
			name: "missing agent is false",
			cfg:  &AgentConfig{},
		},
		{
			name: "rich without agent enable is false",
			cfg:  &AgentConfig{Agent: &AgentOptions{Rich: true}},
		},
		{
			name: "enabled rich agent is true",
			cfg:  &AgentConfig{Agent: &AgentOptions{Enable: true, Rich: true}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cfg.IsAgentV3RichEnabled())
		})
	}
}

func TestAgentV3CheckConfigNormalizesFixedRuntimeSurface(t *testing.T) {
	cfg := &AgentV3Config{
		Memory: AgentV3MemoryConfig{
			Scope:       "global",
			AllowGlobal: true,
			WritePolicy: "model_auto",
		},
		Runtime: AgentV3RuntimeConfig{
			Mode:           "host",
			NamespaceScope: "global",
		},
		Tools: AgentV3ToolsConfig{
			ExposeOnly: []string{"read", "bash", "mcp_search"},
		},
		Skills: AgentV3SkillsConfig{
			Mode: "runtime_filesystem",
			Root: "/tmp/skills",
		},
	}
	cfg.checkConfig()

	assert.Equal(t, "group", cfg.Memory.Scope)
	assert.False(t, cfg.Memory.AllowGlobal)
	assert.Equal(t, "explicit_or_admin", cfg.Memory.WritePolicy)
	assert.Equal(t, "remote_http", cfg.Runtime.Mode)
	assert.Equal(t, "group", cfg.Runtime.NamespaceScope)
	assert.Equal(t, []string{"read", "grep", "write", "edit", "bash"}, cfg.Tools.ExposeOnly)
	assert.Equal(t, "system_prompt", cfg.Skills.Mode)
	assert.Equal(t, "/tmp/skills", cfg.Skills.Root)
	require.NotNil(t, cfg.Skills.InjectBuiltin)
	assert.True(t, *cfg.Skills.InjectBuiltin)
}

func TestAgentV3SkillsDefaultsRemainClosedAndRootIsPreserved(t *testing.T) {
	cfg := &AgentV3Config{
		Skills: AgentV3SkillsConfig{Root: "/mounted/skills"},
	}
	cfg.checkConfig()

	assert.Equal(t, "/mounted/skills", cfg.Skills.Root)
	assert.False(t, cfg.Skills.RuntimeGlobal)
	require.NotNil(t, cfg.Skills.InjectBuiltin)
	assert.True(t, *cfg.Skills.InjectBuiltin)
	assert.False(t, cfg.Skills.SearXNG.Enable)
}

func TestAgentV3SearXNGDefaults(t *testing.T) {
	cfg := &AgentV3Config{}
	cfg.checkConfig()

	searxng := cfg.Skills.SearXNG
	assert.False(t, searxng.Enable)
	assert.Empty(t, searxng.BaseURL)
	assert.Empty(t, searxng.UsernameEnv)
	assert.Empty(t, searxng.PasswordEnv)
	assert.Equal(t, "10s", searxng.Timeout)
	assert.Equal(t, int64(1024*1024), searxng.MaxResponseBytes)
	assert.Equal(t, 10, searxng.MaxResults)
	assert.Equal(t, 2000, searxng.MaxResultChars)
	assert.Equal(t, "zh-CN", searxng.DefaultLanguage)
	assert.Zero(t, searxng.DefaultSafeSearch)
	assert.Equal(t, "text", searxng.DefaultResponseFormat)
	assert.Equal(t, "csust-got-agent-v3", searxng.UserAgent)
	assert.Equal(t, 10*time.Second, cfg.SearXNGTimeout())
}

func TestAgentV3ValidateSearXNGSkipsDisabledConfiguration(t *testing.T) {
	cfg := &AgentV3Config{
		Skills: AgentV3SkillsConfig{
			SearXNG: AgentV3SearXNGConfig{
				BaseURL:               "ftp://user:password@example.org/?query#fragment",
				UsernameEnv:           "1INVALID",
				Timeout:               "0s",
				MaxResponseBytes:      -1,
				MaxResults:            -1,
				MaxResultChars:        -1,
				DefaultLanguage:       "\x00",
				DefaultSafeSearch:     -1,
				DefaultResponseFormat: "xml",
				UserAgent:             "\r\n",
			},
		},
	}

	assert.NoError(t, cfg.ValidateSearXNG())
}

func TestAgentV3ValidateSearXNGAcceptsExactBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		searxng AgentV3SearXNGConfig
	}{
		{
			name: "lower bounds with path prefix and credentials",
			searxng: AgentV3SearXNGConfig{
				Enable:                true,
				BaseURL:               "https://search.example.org/prefix",
				UsernameEnv:           "_",
				PasswordEnv:           "PASSWORD_1",
				Timeout:               "1ms",
				MaxResponseBytes:      1,
				MaxResults:            1,
				MaxResultChars:        1,
				DefaultLanguage:       "a",
				DefaultSafeSearch:     0,
				DefaultResponseFormat: "text",
				UserAgent:             "a",
			},
		},
		{
			name: "upper bounds without credentials",
			searxng: AgentV3SearXNGConfig{
				Enable:                true,
				BaseURL:               "http://search.example.org/prefix",
				Timeout:               "30s",
				MaxResponseBytes:      5 * 1024 * 1024,
				MaxResults:            20,
				MaxResultChars:        16384,
				DefaultLanguage:       strings.Repeat("界", 64),
				DefaultSafeSearch:     2,
				DefaultResponseFormat: "json",
				UserAgent:             strings.Repeat("a", 512),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &AgentV3Config{Skills: AgentV3SkillsConfig{SearXNG: tt.searxng}}
			require.NoError(t, cfg.ValidateSearXNG())
		})
	}
}

func TestAgentV3ValidateSearXNGRejectsInvalidEnabledConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentV3SearXNGConfig)
	}{
		{name: "unsupported URL scheme", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "ftp://search.example.org" }},
		{name: "relative URL", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "/search" }},
		{name: "opaque URL", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "https:search.example.org" }},
		{name: "missing URL host", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "https:///search" }},
		{name: "URL user info", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "https://user:password@search.example.org" }},
		{name: "URL query", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "https://search.example.org?query=value" }},
		{name: "URL fragment", mutate: func(c *AgentV3SearXNGConfig) { c.BaseURL = "https://search.example.org#fragment" }},
		{name: "username without password", mutate: func(c *AgentV3SearXNGConfig) { c.UsernameEnv = "SEARXNG_USERNAME" }},
		{name: "password without username", mutate: func(c *AgentV3SearXNGConfig) { c.PasswordEnv = "SEARXNG_PASSWORD" }},
		{name: "invalid username environment name", mutate: func(c *AgentV3SearXNGConfig) { c.UsernameEnv, c.PasswordEnv = "1INVALID", "SEARXNG_PASSWORD" }},
		{name: "invalid password environment name", mutate: func(c *AgentV3SearXNGConfig) { c.UsernameEnv, c.PasswordEnv = "SEARXNG_USERNAME", "INVALID-NAME" }},
		{name: "timeout below minimum", mutate: func(c *AgentV3SearXNGConfig) { c.Timeout = "999us" }},
		{name: "timeout above maximum", mutate: func(c *AgentV3SearXNGConfig) { c.Timeout = "30001ms" }},
		{name: "invalid timeout", mutate: func(c *AgentV3SearXNGConfig) { c.Timeout = "invalid" }},
		{name: "body below minimum", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResponseBytes = 0 }},
		{name: "body above maximum", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResponseBytes = 5*1024*1024 + 1 }},
		{name: "results below minimum", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResults = 0 }},
		{name: "results above maximum", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResults = 21 }},
		{name: "result characters below minimum", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResultChars = 0 }},
		{name: "result characters above maximum", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResultChars = 16385 }},
		{name: "result characters exceed response budget", mutate: func(c *AgentV3SearXNGConfig) { c.MaxResponseBytes, c.MaxResultChars = 100, 101 }},
		{name: "empty language", mutate: func(c *AgentV3SearXNGConfig) { c.DefaultLanguage = "" }},
		{name: "language exceeds rune limit", mutate: func(c *AgentV3SearXNGConfig) { c.DefaultLanguage = strings.Repeat("界", 65) }},
		{name: "language contains control character", mutate: func(c *AgentV3SearXNGConfig) { c.DefaultLanguage = "zh\nCN" }},
		{name: "safe search below minimum", mutate: func(c *AgentV3SearXNGConfig) { c.DefaultSafeSearch = -1 }},
		{name: "safe search above maximum", mutate: func(c *AgentV3SearXNGConfig) { c.DefaultSafeSearch = 3 }},
		{name: "unsupported response format", mutate: func(c *AgentV3SearXNGConfig) { c.DefaultResponseFormat = "xml" }},
		{name: "empty user agent", mutate: func(c *AgentV3SearXNGConfig) { c.UserAgent = "" }},
		{name: "user agent exceeds byte limit", mutate: func(c *AgentV3SearXNGConfig) { c.UserAgent = strings.Repeat("a", 513) }},
		{name: "user agent contains carriage return", mutate: func(c *AgentV3SearXNGConfig) { c.UserAgent = "agent\rvalue" }},
		{name: "user agent contains newline", mutate: func(c *AgentV3SearXNGConfig) { c.UserAgent = "agent\nvalue" }},
		{name: "user agent contains control character", mutate: func(c *AgentV3SearXNGConfig) { c.UserAgent = "agent\tvalue" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searxng := validAgentV3SearXNGConfig()
			tt.mutate(&searxng)

			cfg := &AgentV3Config{Skills: AgentV3SkillsConfig{SearXNG: searxng}}
			require.Error(t, cfg.ValidateSearXNG())
		})
	}
}

func validAgentV3SearXNGConfig() AgentV3SearXNGConfig {
	return AgentV3SearXNGConfig{
		Enable:                true,
		BaseURL:               "https://search.example.org",
		Timeout:               "10s",
		MaxResponseBytes:      1024 * 1024,
		MaxResults:            10,
		MaxResultChars:        2000,
		DefaultLanguage:       "zh-CN",
		DefaultSafeSearch:     0,
		DefaultResponseFormat: "text",
		UserAgent:             "csust-got-agent-v3",
	}
}

func TestAgentV3RuntimeFetchDefaultsDisabled(t *testing.T) {
	var nilConfig *AgentV3Config
	assert.False(t, nilConfig.RuntimeFetchEnabled())

	omitted := &AgentV3Config{}
	assert.False(t, omitted.RuntimeFetchEnabled())
	omitted.checkConfig()
	assert.False(t, omitted.RuntimeFetchEnabled())
}

func TestAgentV3RuntimeFetchRequiresExplicitTrue(t *testing.T) {
	cfg := &AgentV3Config{}

	disabled := false
	cfg.Runtime.FetchEnabled = &disabled
	assert.False(t, cfg.RuntimeFetchEnabled())

	enabled := true
	cfg.Runtime.FetchEnabled = &enabled
	assert.True(t, cfg.RuntimeFetchEnabled())
}

func TestAgentV3RuntimeEnvYAMLIsLiteral(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
agent_v3:
  runtime:
    env:
      ApiToken: "${SECRET_TOKEN}"
      EMPTY: ""
      WEBHOOK_URL: "https://example.org/?a=1&b=2"
`)))

	var cfg AgentV3Config
	require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
	require.Equal(t, map[string]string{
		"apitoken":    "${SECRET_TOKEN}",
		"empty":       "",
		"webhook_url": "https://example.org/?a=1&b=2",
	}, cfg.Runtime.Env)
	// Viper normalizes YAML keys to lowercase before unmarshalling maps.
	require.NoError(t, ValidateAgentV3RuntimeEnv(cfg.Runtime.Env))
	direct := map[string]string{"ApiToken": "literal"}
	require.NoError(t, ValidateAgentV3RuntimeEnv(direct))
	require.Equal(t, "literal", direct["ApiToken"])
}

func TestValidateAgentV3RuntimeEnvLimits(t *testing.T) {
	require.NoError(t, ValidateAgentV3RuntimeEnv(nil))
	require.NoError(t, ValidateAgentV3RuntimeEnv(map[string]string{
		"_": "", strings.Repeat("A", 128): strings.Repeat("界", 682) + "aa",
	}))

	valid := make(map[string]string, 64)
	for i := range 64 {
		valid[fmt.Sprintf("ITEM_%d", i)] = "x"
	}
	require.NoError(t, ValidateAgentV3RuntimeEnv(valid))
	valid["ITEM_64"] = "x"
	require.ErrorContains(t, ValidateAgentV3RuntimeEnv(valid), "runtime_env_limit")

	budget := map[string]string{}
	for _, name := range []string{"A", "B", "C", "D"} {
		budget[name] = strings.Repeat("a", 2037)
	}
	budget["A"] += "aaaaaaa"
	require.NoError(t, ValidateAgentV3RuntimeEnv(budget)) // Serialized [[name,value],...] is exactly 8192 bytes.
	budget["A"] += "a"
	require.ErrorContains(t, ValidateAgentV3RuntimeEnv(budget), "runtime_env_limit")
	budget["A"] = strings.Repeat("a", 2043) + "&"
	require.NoError(t, ValidateAgentV3RuntimeEnv(budget)) // Rust does not HTML-escape &.
	budget["A"] = strings.Repeat("\u2028", 681) + "&"
	require.NoError(t, ValidateAgentV3RuntimeEnv(budget)) // Rust does not escape U+2028.
	budget["A"] = strings.Repeat("\n", 2038)
	require.ErrorContains(t, ValidateAgentV3RuntimeEnv(budget), "runtime_env_limit")
}

func TestValidateAgentV3RuntimeEnvLayersCountsBeforeOverride(t *testing.T) {
	first := make(map[string]string)
	for i := range 64 {
		first[fmt.Sprintf("ITEM_%d", i)] = "x"
	}
	require.NoError(t, ValidateAgentV3RuntimeEnvLayers(first))
	require.ErrorContains(t, ValidateAgentV3RuntimeEnvLayers(first, map[string]string{"ITEM_0": "new"}), "runtime_env_limit")
	require.ErrorContains(t, ValidateAgentV3RuntimeEnvLayers(
		map[string]string{"A": strings.Repeat("x", 2048), "B": strings.Repeat("x", 2048)},
		map[string]string{"C": strings.Repeat("x", 2048), "D": strings.Repeat("x", 2048)},
	), "runtime_env_limit")
}

func TestValidateAgentV3RuntimeEnvRejectsInvalidNamesAndValues(t *testing.T) {
	for _, name := range []string{"", "1ABC", "A-B", "A.B", "A B", "é", strings.Repeat("A", 129)} {
		t.Run(fmt.Sprintf("invalid name %q", name), func(t *testing.T) {
			require.ErrorContains(t, ValidateAgentV3RuntimeEnv(map[string]string{name: "secret-value"}), "runtime_env_name")
		})
	}
	reserved := []string{
		"PATH", "HOME", "SHELL", "ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "IFS",
		"CDPATH", "PWD", "OLDPWD", "SHLVL", "PS4", "PROMPT_COMMAND", "GLOBIGNORE",
		"TMPDIR", "TMP", "TEMP", "GLIBC_TUNABLES", "GCONV_PATH", "LOCPATH", "NLSPATH",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	}
	for _, name := range reserved {
		t.Run("reserved "+name, func(t *testing.T) {
			require.ErrorContains(t, ValidateAgentV3RuntimeEnv(map[string]string{strings.ToLower(name): "secret-value"}), "runtime_env_reserved")
		})
	}
	for _, prefix := range []string{
		"AGENT_RUNTIME_", "AGENT_FETCH_", "LD_", "DYLD_", "BASH_", "PROOT_", "MALLOC_",
		"GIT_", "PYTHON", "PERL", "RUBY", "NODE_", "JAVA_", "JDK_", "_JAVA_", "SSL_",
		"CURL_", "WGET_",
	} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			require.ErrorContains(t, ValidateAgentV3RuntimeEnv(map[string]string{strings.ToLower(prefix) + "X": "secret-value"}), "runtime_env_reserved")
		})
	}
	for _, name := range []string{"PYTHO", "GIT", "NODE", "SSL", "TEMP_FILE"} {
		require.NoError(t, ValidateAgentV3RuntimeEnv(map[string]string{name: "literal"}))
	}
	for _, value := range []string{strings.Repeat("a", 2049), strings.Repeat("界", 683), "before\x00after", string([]byte{0xff})} {
		err := ValidateAgentV3RuntimeEnv(map[string]string{"CREDENTIAL": value})
		require.ErrorContains(t, err, "runtime_env_value")
		require.NotContains(t, err.Error(), value)
	}
}

func TestAgentContextMode(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		reply bool
		bad   bool
	}{
		{name: "default", mode: ""},
		{name: "chat", mode: "chat"},
		{name: "reply chain", mode: "reply_chain", reply: true},
		{name: "legacy reply is rejected", mode: "reply", bad: true},
		{name: "case is rejected", mode: "CHAT", bad: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &AgentConfig{Name: "session", ContextMode: tt.mode}
			assert.Equal(t, tt.reply, cfg.UsesReplyChain())
			if tt.bad {
				require.ErrorContains(t, cfg.ValidateContextMode(), "context_mode")
				return
			}
			require.NoError(t, cfg.ValidateContextMode())
		})
	}

	t.Run("yaml", func(t *testing.T) {
		v := viper.New()
		v.SetConfigType("yaml")
		require.NoError(t, v.ReadConfig(strings.NewReader(`
agents:
  - name: reply-assistant
    context_mode: reply_chain
    message_context: 7
`)))

		var agents AgentV3Configs
		require.NoError(t, v.UnmarshalKey("agents", &agents, viper.DecodeHook(DispatchFor())))
		require.Len(t, agents, 1)
		assert.True(t, agents[0].UsesReplyChain())
		assert.Equal(t, 7, agents[0].MessageContext)
	})
}
