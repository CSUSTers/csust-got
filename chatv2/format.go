package chatv2

import (
	"regexp"
	"strings"

	"csust-got/config"
	"csust-got/util"

	"go.uber.org/zap"
)

// extractReasonPatt matches <think>...</think> blocks at the start of output.
var extractReasonPatt = regexp.MustCompile(`(?si)^\s*<think>\s*(?P<reason>.*?)(?:\s*</think>|$)\s*`)
var reasonGroup = extractReasonPatt.SubexpIndex("reason")

// FormatOutputWithReason formats output text with reasoning content according to config.
// Handles both native reasoning (from model protocol) and parsed <think> tags.
func FormatOutputWithReason(text string, nativeReason string, format *config.ChatOutputFormatConfig) string {
	var reason, payload string

	if format.GetUseNativeReasoning() {
		reason = nativeReason
		payload = text
	} else {
		matches := extractReasonPatt.FindStringSubmatchIndex(text)
		if len(matches) != 0 {
			payload = text[matches[1]:]
			rIdx1, rIdx2 := matches[reasonGroup*2], matches[reasonGroup*2+1]
			reason = text[rIdx1:rIdx2]
		} else {
			payload = text
		}
	}

	buf := strings.Builder{}

	outputFormat := format.GetFormat()
	if outputFormat == "" {
		zap.L().Warn("chatv2: text output format empty, defaulting to markdown")
		outputFormat = "markdown"
	}

	if reason != "" {
		reasonFormat := format.GetReasonFormat()
		if reasonFormat == "" {
			zap.L().Warn("chatv2: reason format empty, defaulting to none")
			reasonFormat = "none"
		}
		switch reasonFormat {
		case "quote":
			formatText(&buf, reason, outputFormat, wholeTextTypeQuote)
		case "collapse":
			formatText(&buf, reason, outputFormat, wholeTextTypeCollapse)
		default:
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
	}

	payloadFormat := format.GetPayloadFormat()
	if payloadFormat == "" {
		zap.L().Warn("chatv2: payload format empty, defaulting to plain")
		payloadFormat = "plain"
	}

	payloadType := wholeTextTypePlain
	switch payloadFormat {
	case "quote":
		payloadType = wholeTextTypeQuote
	case "collapse":
		payloadType = wholeTextTypeCollapse
	case "block":
		payloadType = wholeTextTypeBlock
	case "markdown-block":
		payloadType = wholeTextTypeMdBlock
	}

	formatText(&buf, payload, outputFormat, payloadType)
	return buf.String()
}

type wholeTextType string

const (
	wholeTextTypePlain    wholeTextType = "plain"
	wholeTextTypeQuote    wholeTextType = "quote"
	wholeTextTypeCollapse wholeTextType = "collapse"
	wholeTextTypeBlock    wholeTextType = "block"
	wholeTextTypeMdBlock  wholeTextType = "markdown-block"
)

func formatText(buf *strings.Builder, text string, format string, t wholeTextType) {
	if len(text) == 0 {
		return
	}
	switch format {
	case "markdown":
		switch t {
		case wholeTextTypePlain:
			buf.WriteString(util.EscapeTgMDv2ReservedChars(text))
		case wholeTextTypeCollapse:
			buf.WriteString("**")
			fallthrough
		case wholeTextTypeQuote:
			lines := strings.Lines(text)
			for line := range lines {
				buf.WriteString(">")
				buf.WriteString(util.EscapeTgMDv2ReservedChars(line))
			}
			if t == wholeTextTypeCollapse {
				if text[len(text)-1] == '\n' {
					buf.WriteString(">")
				}
				buf.WriteString("||")
			}
			buf.WriteString("\n")
		case wholeTextTypeBlock, wholeTextTypeMdBlock:
			buf.WriteString("```")
			if t == wholeTextTypeMdBlock {
				buf.WriteString("markdown")
			}
			buf.WriteString("\n")
			buf.WriteString(util.EscapeTgMDv2ReservedChars(text))
			buf.WriteString("\n```\n")
		}
	case "html":
		switch t {
		case wholeTextTypePlain:
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
		case wholeTextTypeCollapse:
			buf.WriteString("<blockquote expandable>")
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
			buf.WriteString("</blockquote>")
		case wholeTextTypeQuote:
			buf.WriteString("<blockquote>")
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
			buf.WriteString("</blockquote>")
		case wholeTextTypeBlock, wholeTextTypeMdBlock:
			buf.WriteString("<pre>")
			if t == wholeTextTypeMdBlock {
				buf.WriteString(`<code class="language-markdown">`)
			}
			buf.WriteString(util.EscapeTgHTMLReservedChars(text))
			if t == wholeTextTypeMdBlock {
				buf.WriteString(`</code>`)
			}
			buf.WriteString("</pre>")
		}
	default:
		buf.WriteString(text)
	}
}

// GetParseMode returns the telebot parse mode string for the format config.
func GetParseMode(format *config.ChatOutputFormatConfig) string {
	switch format.GetFormat() {
	case "html":
		return "HTML"
	case "markdown":
		return "MarkdownV2"
	default:
		return "MarkdownV2"
	}
}

// findLastSentenceDelimiter finds the last occurrence of any sentence delimiter in text.
// Returns the index after the delimiter, or -1 if not found.
func findLastSentenceDelimiter(text string, delimiters []string) int {
	lastIdx := -1
	for _, d := range delimiters {
		if idx := strings.LastIndex(text, d); idx >= 0 {
			end := idx + len(d)
			if end > lastIdx {
				lastIdx = end
			}
		}
	}
	return lastIdx
}
