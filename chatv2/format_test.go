package chatv2

import (
	"strings"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
)

func TestFindLastSentenceDelimiter(t *testing.T) {
	delimiters := []string{".", "!", "?", "\n", "。", "！", "？"}

	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty string", "", -1},
		{"no delimiter", "hello world", -1},
		{"period at end", "hello.", 6},
		{"newline in middle", "hello\nworld", 6},
		{"multiple delimiters", "hello. world!", 13},
		{"chinese period", "你好。世界", len("你好。")},
		{"mixed delimiters picks last", "hello! world?", 13},
		{"newline at end", "hello\n", 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findLastSentenceDelimiter(tt.text, delimiters)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetParseMode(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   string
	}{
		{"html format", "html", "HTML"},
		{"markdown format", "markdown", "MarkdownV2"},
		{"empty defaults to MarkdownV2", "", "MarkdownV2"},
		{"unknown defaults to MarkdownV2", "plaintext", "MarkdownV2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fmt := &config.ChatOutputFormatConfig{Format: tt.format}
			got := GetParseMode(fmt)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		format   string
		whole    wholeTextType
		contains string
	}{
		{"empty text returns empty", "", "html", wholeTextTypePlain, ""},
		{"html plain", "hello <b>world</b>", "html", wholeTextTypePlain, "hello &lt;b&gt;world&lt;/b&gt;"},
		{"html quote", "hello", "html", wholeTextTypeQuote, "<blockquote>hello</blockquote>"},
		{"html collapse", "hello", "html", wholeTextTypeCollapse, "<blockquote expandable>hello</blockquote>"},
		{"html block", "hello", "html", wholeTextTypeBlock, "<pre>hello</pre>"},
		{"html markdown-block", "hello", "html", wholeTextTypeMdBlock, `<code class="language-markdown">`},
		{"html markdown-block closing", "hello", "html", wholeTextTypeMdBlock, "</code>"},
		{"markdown plain", "hello*world", "markdown", wholeTextTypePlain, "hello\\*world"},
		{"markdown block", "hello", "markdown", wholeTextTypeBlock, "```\nhello\n```"},
		{"markdown md-block", "hello", "markdown", wholeTextTypeMdBlock, "```markdown"},
		{"unknown format passes through", "hello", "unknown", wholeTextTypePlain, "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			formatText(&buf, tt.text, tt.format, tt.whole)
			if tt.text == "" {
				assert.Empty(t, buf.String())
			} else {
				assert.Contains(t, buf.String(), tt.contains)
			}
		})
	}
}

func TestFormatOutputWithReason(t *testing.T) {
	useNative := true
	noNative := false

	tests := []struct {
		name         string
		text         string
		nativeReason string
		format       *config.ChatOutputFormatConfig
		wantContains []string
		wantEmpty    bool
	}{
		{
			name: "plain text no reason html",
			text: "hello world",
			format: &config.ChatOutputFormatConfig{
				Format:  "html",
				Payload: "plain",
			},
			wantContains: []string{"hello world"},
		},
		{
			name:         "native reasoning with quote format",
			text:         "answer here",
			nativeReason: "thinking about it",
			format: &config.ChatOutputFormatConfig{
				Format:             "html",
				Reason:             "quote",
				Payload:            "plain",
				UseNativeReasoning: &useNative,
			},
			wantContains: []string{"<blockquote>", "thinking about it", "answer here"},
		},
		{
			name:         "native reasoning disabled parses think tags",
			text:         "<think>my reasoning</think>answer here",
			nativeReason: "",
			format: &config.ChatOutputFormatConfig{
				Format:             "html",
				Reason:             "quote",
				Payload:            "plain",
				UseNativeReasoning: &noNative,
			},
			wantContains: []string{"my reasoning", "answer here"},
		},
		{
			name:         "no native reason and no think tags",
			text:         "just plain text",
			nativeReason: "",
			format: &config.ChatOutputFormatConfig{
				Format:             "html",
				Payload:            "plain",
				UseNativeReasoning: &noNative,
			},
			wantContains: []string{"just plain text"},
		},
		{
			name: "payload block format",
			text: "code output",
			format: &config.ChatOutputFormatConfig{
				Format:  "html",
				Payload: "block",
			},
			wantContains: []string{"<pre>", "code output", "</pre>"},
		},
		{
			name: "empty text",
			text: "",
			format: &config.ChatOutputFormatConfig{
				Format:  "html",
				Payload: "plain",
			},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatOutputWithReason(tt.text, tt.nativeReason, tt.format)
			if tt.wantEmpty {
				assert.Empty(t, got)
				return
			}
			for _, s := range tt.wantContains {
				assert.Contains(t, got, s)
			}
		})
	}
}