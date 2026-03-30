package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

func TestEscapeTgTextByParseMode(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		parseMode tb.ParseMode
		want      string
	}{
		{
			name:      "markdown v2 escapes reserved chars",
			text:      `a_b.c[de](f)!`,
			parseMode: tb.ModeMarkdownV2,
			want:      `a\_b\.c\[de\]\(f\)\!`,
		},
		{
			name:      "html escapes angle brackets and ampersand",
			text:      `a<b>&c`,
			parseMode: tb.ModeHTML,
			want:      `a&lt;b&gt;&amp;c`,
		},
		{
			name:      "unknown mode passes through",
			text:      `a_b`,
			parseMode: "",
			want:      `a_b`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EscapeTgTextByParseMode(tt.text, tt.parseMode)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeTgMessage(t *testing.T) {
	tests := []struct {
		name string
		what any
		ops  []any
		want any
	}{
		{
			name: "string is escaped with markdown v2 parse mode",
			what: `a_b`,
			ops:  []any{tb.ModeMarkdownV2},
			want: `a\_b`,
		},
		{
			name: "raw text is not escaped again",
			what: RawTgText(`a_b`),
			ops:  []any{tb.ModeMarkdownV2},
			want: `a_b`,
		},
		{
			name: "html string is escaped",
			what: `a<b>`,
			ops:  []any{&tb.SendOptions{ParseMode: tb.ModeHTML}},
			want: `a&lt;b&gt;`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTgMessage(tt.what, tt.ops...)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractParseMode(t *testing.T) {
	tests := []struct {
		name string
		ops  []any
		want tb.ParseMode
	}{
		{
			name: "from parse mode arg",
			ops:  []any{tb.ModeMarkdownV2},
			want: tb.ModeMarkdownV2,
		},
		{
			name: "from send options pointer",
			ops:  []any{&tb.SendOptions{ParseMode: tb.ModeHTML}},
			want: tb.ModeHTML,
		},
		{
			name: "from send options value",
			ops:  []any{tb.SendOptions{ParseMode: tb.ModeMarkdownV2}},
			want: tb.ModeMarkdownV2,
		},
		{
			name: "last non-empty parse mode wins",
			ops:  []any{tb.ModeHTML, &tb.SendOptions{ParseMode: tb.ModeMarkdownV2}},
			want: tb.ModeMarkdownV2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractParseMode(tt.ops...)
			assert.Equal(t, tt.want, got)
		})
	}
}
