package chat

import (
	"strings"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/util"

	"github.com/stretchr/testify/assert"
)

func TestFindLastSentenceDelimiter(t *testing.T) {
	delimiters := []string{"\n", ".", "!", "?", "。", "！", "？", ")", "）", ";", "..."}

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "text with period",
			text:     "Hello world. This is a test",
			expected: 12, // Position after '.'
		},
		{
			name:     "text with newline",
			text:     "Hello world\nThis is a test",
			expected: 12, // Position after '\n'
		},
		{
			name:     "text with multiple delimiters",
			text:     "Hello world. This is a test! How are you?",
			expected: 41, // Position after last '?'
		},
		{
			name:     "text with no delimiters",
			text:     "Hello world without any punctuation",
			expected: -1,
		},
		{
			name:     "text with Chinese punctuation",
			text:     "你好世界。这是一个测试！",
			expected: 36, // Position after '！' (index in bytes)
		},
		{
			name:     "empty text",
			text:     "",
			expected: -1,
		},
		{
			name:     "text with multi-character delimiter",
			text:     "This is interesting... What do you think?",
			expected: 41, // Position after '?'
		},
		{
			name:     "text with only multi-character delimiter",
			text:     "This is interesting...",
			expected: 22, // Position after '...'
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findLastSentenceDelimiter(tt.text, delimiters)
			if result != tt.expected {
				t.Errorf("findLastSentenceDelimiter() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestChatOutputFormatConfig_GetEditInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{
			name:     "valid duration string",
			interval: "750ms",
			expected: 750 * time.Millisecond,
		},
		{
			name:     "valid second duration",
			interval: "2s",
			expected: 2 * time.Second,
		},
		{
			name:     "empty string uses default",
			interval: "",
			expected: time.Second,
		},
		{
			name:     "invalid string uses default",
			interval: "invalid",
			expected: time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ChatOutputFormatConfig{
				EditInterval: tt.interval,
			}
			result := cfg.GetEditInterval()
			if result != tt.expected {
				t.Errorf("GetEditInterval() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestFormatOutputWithReason(t *testing.T) {
	boolTrue := true
	boolFalse := false

	tests := []struct {
		name         string
		text         string
		nativeReason string
		format       *config.ChatOutputFormatConfig
		wantContains []string
	}{
		{
			name:         "native reasoning content with collapse format",
			text:         "Hello world",
			nativeReason: "Let me think about this...",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "collapse",
				UseNativeReasoning: &boolTrue,
			},
			wantContains: []string{"Let me think about this", "Hello world"},
		},
		{
			name:         "native reasoning content with quote format",
			text:         "Hello world",
			nativeReason: "Let me think about this...",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "quote",
				UseNativeReasoning: &boolTrue,
			},
			wantContains: []string{">", "Let me think about this", "Hello world"},
		},
		{
			name:         "native reasoning content with none format - reason hidden",
			text:         "Hello world",
			nativeReason: "Let me think about this...",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "none",
				UseNativeReasoning: &boolTrue,
			},
			wantContains: []string{"Hello world"},
		},
		{
			name:         "legacy mode: parse think tags when use_native_reasoning is false",
			text:         "<think>This is my reasoning</think>And the answer is 42",
			nativeReason: "",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "collapse",
				UseNativeReasoning: &boolFalse,
			},
			wantContains: []string{"This is my reasoning", "And the answer is 42"},
		},
		{
			name:         "legacy mode: no reasoning in text",
			text:         "Just a plain response",
			nativeReason: "",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "collapse",
				UseNativeReasoning: &boolFalse,
			},
			wantContains: []string{"Just a plain response"},
		},
		{
			name:         "native mode ignores think tags in text",
			text:         "<think>Old reasoning</think>Some content",
			nativeReason: "Native reasoning",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "quote",
				UseNativeReasoning: &boolTrue,
			},
			// Native mode uses nativeReason, text with <think> tags is kept as-is (escaped)
			wantContains: []string{"Native reasoning", "Old reasoning", "Some content"},
		},
		{
			name:         "HTML format with native reasoning",
			text:         "Response text",
			nativeReason: "My thoughts",
			format: &config.ChatOutputFormatConfig{
				Format:             "html",
				Reason:             "collapse",
				UseNativeReasoning: &boolTrue,
			},
			wantContains: []string{"blockquote expandable", "My thoughts", "Response text"},
		},
		{
			name:         "default config (nil) uses native reasoning",
			text:         "Hello world",
			nativeReason: "Native thoughts",
			format: &config.ChatOutputFormatConfig{
				Format: "markdown",
				Reason: "collapse",
				// UseNativeReasoning is nil, should default to true
			},
			wantContains: []string{"Native thoughts", "Hello world"},
		},
		{
			name:         "legacy mode with think tags and native reason provided - uses tags",
			text:         "<think>Tag reasoning</think>Content",
			nativeReason: "Native reasoning",
			format: &config.ChatOutputFormatConfig{
				Format:             "markdown",
				Reason:             "collapse",
				UseNativeReasoning: &boolFalse,
			},
			// Legacy mode parses <think> tags, ignores nativeReason
			wantContains: []string{"Tag reasoning", "Content"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatOutputWithReason(tt.text, tt.nativeReason, tt.format)
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("formatOutputWithReason() = %q, want it to contain %q", result, want)
				}
			}
		})
	}
}

func TestFormatOutputWithReason_EscapesFinalTelegramTextOnce(t *testing.T) {
	boolTrue := true

	tests := []struct {
		name     string
		text     string
		format   *config.ChatOutputFormatConfig
		expected string
	}{
		{
			name:     "markdown plain text is escaped once",
			text:     "a.b _x_ [y](z) !",
			format:   &config.ChatOutputFormatConfig{Format: "markdown", Payload: "plain", UseNativeReasoning: &boolTrue},
			expected: util.EscapeTgMDv2ReservedChars("a.b _x_ [y](z) !"),
		},
		{
			name:     "markdown block keeps wrapper raw and escapes inner text once",
			text:     "line.1\n<tag>",
			format:   &config.ChatOutputFormatConfig{Format: "markdown", Payload: "markdown-block", UseNativeReasoning: &boolTrue},
			expected: "```markdown\n" + util.EscapeTgMDv2ReservedChars("line.1\n<tag>") + "\n```\n",
		},
		{
			name:     "html plain text is escaped once",
			text:     "a<b>&c",
			format:   &config.ChatOutputFormatConfig{Format: "html", Payload: "plain", UseNativeReasoning: &boolTrue},
			expected: util.EscapeTgHTMLReservedChars("a<b>&c"),
		},
		{
			name:     "html block keeps wrapper raw and escapes inner text once",
			text:     "line.1\n<tag>&value",
			format:   &config.ChatOutputFormatConfig{Format: "html", Payload: "block", UseNativeReasoning: &boolTrue},
			expected: "<pre>" + util.EscapeTgHTMLReservedChars("line.1\n<tag>&value") + "</pre>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatOutputWithReason(tt.text, "", tt.format)
			assert.Equal(t, tt.expected, got)
			assert.NotContains(t, got, "&amp;amp;")
			assert.NotContains(t, got, "\\\\.")
		})
	}
}
