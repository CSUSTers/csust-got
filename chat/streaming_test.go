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
