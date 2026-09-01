//go:build !386 && !arm

package agentv3

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
			fmt := &config.AgentOutputConfig{Format: tt.format}
			got := GetParseMode(fmt)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatTextEscapesReservedChars(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		format string
		whole  wholeTextType
		want   string
	}{
		{
			name:   "markdown plain escapes reserved chars",
			text:   "a.b_c[1](2)!",
			format: "markdown",
			whole:  wholeTextTypePlain,
			want:   "a\\.b\\_c\\[1\\]\\(2\\)\\!",
		},
		{
			name:   "markdown code block escapes reserved chars",
			text:   "a.b_c",
			format: "markdown",
			whole:  wholeTextTypeBlock,
			want:   "```\na\\.b\\_c\n```\n",
		},
		{
			name:   "html plain escapes tags and ampersand",
			text:   "<b>a&b</b>",
			format: "html",
			whole:  wholeTextTypePlain,
			want:   "&lt;b&gt;a&amp;b&lt;/b&gt;",
		},
		{
			name:   "html markdown block wraps code tag",
			text:   "line 1",
			format: "html",
			whole:  wholeTextTypeMdBlock,
			want:   "<pre><code class=\"language-markdown\">line 1</code></pre>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			formatText(&buf, tt.text, tt.format, tt.whole)
			assert.Equal(t, tt.want, buf.String())
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
		{"markdown plain escapes dot", "a.b", "markdown", wholeTextTypePlain, "a\\.b"},
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

func TestFormatOutputWithReasonEscapesReservedChars(t *testing.T) {
	useNative := true

	tests := []struct {
		name         string
		text         string
		nativeReason string
		format       *config.AgentOutputConfig
		wantContains []string
	}{
		{
			name:         "markdown escapes payload and reasoning",
			text:         "answer.a",
			nativeReason: "think_b",
			format: &config.AgentOutputConfig{
				Format:             "markdown",
				Reason:             "quote",
				Payload:            "plain",
				UseNativeReasoning: &useNative,
			},
			wantContains: []string{"think\\_b", "answer\\.a"},
		},
		{
			name:         "html escapes payload and reasoning",
			text:         "answer<a>",
			nativeReason: "think&b",
			format: &config.AgentOutputConfig{
				Format:             "html",
				Reason:             "quote",
				Payload:            "plain",
				UseNativeReasoning: &useNative,
			},
			wantContains: []string{"think&amp;b", "answer&lt;a&gt;"},
		},
		{
			name:         "markdown block escapes content",
			text:         "code.a",
			nativeReason: "",
			format: &config.AgentOutputConfig{
				Format:             "markdown",
				Payload:            "block",
				UseNativeReasoning: &useNative,
			},
			wantContains: []string{"```", "code\\.a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatOutputWithReason(tt.text, tt.nativeReason, tt.format)
			for _, s := range tt.wantContains {
				assert.Contains(t, got, s)
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
		format       *config.AgentOutputConfig
		wantContains []string
		wantEmpty    bool
	}{
		{
			name: "plain text no reason html",
			text: "hello world",
			format: &config.AgentOutputConfig{
				Format:  "html",
				Payload: "plain",
			},
			wantContains: []string{"hello world"},
		},
		{
			name:         "native reasoning with quote format",
			text:         "answer here",
			nativeReason: "thinking about it",
			format: &config.AgentOutputConfig{
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
			format: &config.AgentOutputConfig{
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
			format: &config.AgentOutputConfig{
				Format:             "html",
				Payload:            "plain",
				UseNativeReasoning: &noNative,
			},
			wantContains: []string{"just plain text"},
		},
		{
			name: "payload block format",
			text: "code output",
			format: &config.AgentOutputConfig{
				Format:  "html",
				Payload: "block",
			},
			wantContains: []string{"<pre>", "code output", "</pre>"},
		},
		{
			name: "empty text",
			text: "",
			format: &config.AgentOutputConfig{
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
