package agentv3

import (
	"strings"
	"testing"

	"csust-got/config"
	"csust-got/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestTakeTelegramChunkPrefersParagraphsAndAvoidsCodeFences(t *testing.T) {
	limitFits := func(limit int) func(string) bool {
		return func(s string) bool { return util.UTF16Len(s) <= limit }
	}
	tests := []struct {
		name      string
		text      string
		limit     int
		wantChunk string
		wantRest  string
	}{
		{
			name:      "fits whole",
			text:      "short",
			limit:     10,
			wantChunk: "short",
			wantRest:  "",
		},
		{
			name:      "paragraph boundary wins over line boundary",
			text:      "para one\nstill one\n\npara two\nmore",
			limit:     25,
			wantChunk: "para one\nstill one",
			wantRest:  "para two\nmore",
		},
		{
			name:      "line boundary when no paragraph fits",
			text:      "line one\nline two\nline three",
			limit:     20,
			wantChunk: "line one\nline two",
			wantRest:  "line three",
		},
		{
			name:      "boundary inside code fence is avoided",
			text:      "intro\n```\ncode a\n\ncode b\n```\ntail",
			limit:     20,
			wantChunk: "intro",
			wantRest:  "```\ncode a\n\ncode b\n```\ntail",
		},
		{
			name:      "hard split between runes when no boundary",
			text:      "abcdefghij",
			limit:     4,
			wantChunk: "abcd",
			wantRest:  "efghij",
		},
		{
			name:      "hard split respects utf16 width",
			text:      "😀😀😀",
			limit:     3,
			wantChunk: "😀",
			wantRest:  "😀😀",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk, rest := takeTelegramChunk(tt.text, limitFits(tt.limit))
			assert.Equal(t, tt.wantChunk, chunk)
			assert.Equal(t, tt.wantRest, rest)
		})
	}
}

func TestChunkPlainTelegramTextRoundTrips(t *testing.T) {
	text := strings.Repeat("第一行内容。\n", 400)
	chunks := chunkPlainTelegramText(text, 500)
	require.Greater(t, len(chunks), 1)
	for _, chunk := range chunks {
		assert.LessOrEqual(t, util.UTF16Len(chunk), 500)
		assert.NotEmpty(t, strings.TrimSpace(chunk))
	}
	assert.Equal(t, text, strings.Join(chunks, "\n"), "boundary newlines are the only thing removed from each chunk")
	assert.Empty(t, chunkPlainTelegramText("", 500))
	assert.Equal(t, []string{"short\n\n"}, chunkPlainTelegramText("short\n\n", 500), "text that already fits is passed through unchanged")
}

func TestFormatTelegramChunksFormatsEachChunkSeparately(t *testing.T) {
	line := "value.1 (x) [y] _z_"
	text := strings.TrimRight(strings.Repeat(line+"\n", 300), "\n")

	t.Run("markdown plain escapes per chunk", func(t *testing.T) {
		format := &config.AgentOutputConfig{Format: "markdown"}
		chunks := formatTelegramChunks(text, "", format, 1000)
		require.Greater(t, len(chunks), 1)
		formatted := make([]string, 0, len(chunks))
		raw := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			assert.LessOrEqual(t, util.UTF16Len(chunk.formatted), 1000)
			assert.Equal(t, util.EscapeTgMDv2ReservedChars(chunk.raw), chunk.formatted)
			formatted = append(formatted, chunk.formatted)
			raw = append(raw, chunk.raw)
		}
		assert.Equal(t, text, strings.Join(raw, "\n"))
		assert.Equal(t, util.EscapeTgMDv2ReservedChars(text), strings.Join(formatted, "\n"))
	})

	t.Run("markdown block keeps fences balanced", func(t *testing.T) {
		format := &config.AgentOutputConfig{Format: "markdown", Payload: "block"}
		chunks := formatTelegramChunks(text, "", format, 1000)
		require.Greater(t, len(chunks), 1)
		for _, chunk := range chunks {
			assert.LessOrEqual(t, util.UTF16Len(chunk.formatted), 1000)
			assert.True(t, strings.HasPrefix(chunk.formatted, "```\n"))
			assert.True(t, strings.HasSuffix(chunk.formatted, "\n```\n"))
			assert.Equal(t, 2, strings.Count(chunk.formatted, "```"))
		}
	})

	t.Run("html escapes per chunk", func(t *testing.T) {
		format := &config.AgentOutputConfig{Format: "html"}
		htmlText := strings.TrimRight(strings.Repeat("<b> & </b>\n", 300), "\n")
		chunks := formatTelegramChunks(htmlText, "", format, 800)
		require.Greater(t, len(chunks), 1)
		for _, chunk := range chunks {
			assert.LessOrEqual(t, util.UTF16Len(chunk.formatted), 800)
			assert.Equal(t, util.EscapeTgHTMLReservedChars(chunk.raw), chunk.formatted)
		}
	})

	t.Run("short text is a single chunk", func(t *testing.T) {
		chunks := formatTelegramChunks("hello", "", &config.AgentOutputConfig{Format: "markdown"}, 4096)
		require.Len(t, chunks, 1)
		assert.Equal(t, "hello", chunks[0].formatted)
		assert.Equal(t, "hello", chunks[0].raw)
	})

	t.Run("empty formatted output yields no chunks", func(t *testing.T) {
		useNative := false
		assert.Empty(t, formatTelegramChunks("<think>hidden</think>", "", &config.AgentOutputConfig{UseNativeReasoning: &useNative}, 4096))
	})
}

func TestFormatTelegramChunksKeepsReasonOnFirstChunk(t *testing.T) {
	useNative := true
	format := &config.AgentOutputConfig{Format: "markdown", Reason: "quote", UseNativeReasoning: &useNative}
	payload := strings.TrimRight(strings.Repeat("payload line\n", 200), "\n")

	chunks := formatTelegramChunks(payload, "short reason", format, 600)

	require.Greater(t, len(chunks), 1)
	assert.True(t, strings.HasPrefix(chunks[0].formatted, ">short reason"))
	assert.Contains(t, chunks[0].formatted, "payload line")
	assert.True(t, strings.HasPrefix(chunks[0].raw, "short reason\n\n"))
	for _, chunk := range chunks[1:] {
		assert.False(t, strings.Contains(chunk.formatted, "short reason"))
		assert.False(t, strings.HasPrefix(chunk.formatted, ">"))
	}
	for _, chunk := range chunks {
		assert.LessOrEqual(t, util.UTF16Len(chunk.formatted), 600)
	}

	longReason := strings.TrimRight(strings.Repeat("reason line\n", 200), "\n")
	chunks = formatTelegramChunks("tiny payload", longReason, format, 600)
	require.Greater(t, len(chunks), 2)
	for _, chunk := range chunks[:len(chunks)-1] {
		assert.True(t, strings.HasPrefix(chunk.formatted, ">"), "oversized reasoning gets its own quoted chunks")
		assert.LessOrEqual(t, util.UTF16Len(chunk.formatted), 600)
	}
	assert.Equal(t, "tiny payload", chunks[len(chunks)-1].formatted)
}

func TestTailTelegramPreview(t *testing.T) {
	format := &config.AgentOutputConfig{Format: "markdown"}
	short := tailTelegramPreview("hello", "", format, 100)
	assert.Equal(t, "hello", short.formatted)
	assert.Equal(t, "hello", short.raw)

	text := strings.TrimRight(strings.Repeat("0123456789.\n", 100), "\n")
	tail := tailTelegramPreview(text, "", format, 120)
	assert.LessOrEqual(t, util.UTF16Len(tail.formatted), 120)
	assert.True(t, strings.HasPrefix(tail.formatted, "…"))
	assert.True(t, strings.HasPrefix(tail.raw, "…"))
	assert.True(t, strings.HasSuffix(text, strings.TrimPrefix(tail.raw, "…")))
	assert.Equal(t, util.EscapeTgMDv2ReservedChars(tail.raw), tail.formatted)

	assert.Empty(t, tailTelegramPreview(text, "", format, 1).formatted)
}
