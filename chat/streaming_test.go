package chat

import (
	"testing"
	"time"

	"csust-got/config"
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
				Format: "markdown",
				Reason: "collapse",
			},
			wantContains: []string{"Let me think about this", "Hello world"},
		},
		{
			name:         "native reasoning content with quote format",
			text:         "Hello world",
			nativeReason: "Let me think about this...",
			format: &config.ChatOutputFormatConfig{
				Format: "markdown",
				Reason: "quote",
			},
			wantContains: []string{">", "Let me think about this", "Hello world"},
		},
		{
			name:         "native reasoning content with none format - reason hidden",
			text:         "Hello world",
			nativeReason: "Let me think about this...",
			format: &config.ChatOutputFormatConfig{
				Format: "markdown",
				Reason: "none",
			},
			wantContains: []string{"Hello world"},
		},
		{
			name:         "fallback to think tag parsing when no native reason",
			text:         "<think>This is my reasoning</think>And the answer is 42",
			nativeReason: "",
			format: &config.ChatOutputFormatConfig{
				Format: "markdown",
				Reason: "collapse",
			},
			wantContains: []string{"This is my reasoning", "And the answer is 42"},
		},
		{
			name:         "no reasoning in text without native reason",
			text:         "Just a plain response",
			nativeReason: "",
			format: &config.ChatOutputFormatConfig{
				Format: "markdown",
				Reason: "collapse",
			},
			wantContains: []string{"Just a plain response"},
		},
		{
			name:         "native reasoning takes precedence over think tags",
			text:         "<think>Old reasoning</think>Some content",
			nativeReason: "Native reasoning",
			format: &config.ChatOutputFormatConfig{
				Format: "markdown",
				Reason: "quote",
			},
			// The text contains <think> tags, but native reasoning is used. Text is escaped in markdown.
			wantContains: []string{"Native reasoning", "Old reasoning", "Some content"},
		},
		{
			name:         "HTML format with native reasoning",
			text:         "Response text",
			nativeReason: "My thoughts",
			format: &config.ChatOutputFormatConfig{
				Format: "html",
				Reason: "collapse",
			},
			wantContains: []string{"blockquote expandable", "My thoughts", "Response text"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatOutputWithReason(tt.text, tt.nativeReason, tt.format)
			for _, want := range tt.wantContains {
				if !contains(result, want) {
					t.Errorf("formatOutputWithReason() = %q, want it to contain %q", result, want)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr) >= 0))
}

func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
