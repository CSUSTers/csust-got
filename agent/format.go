package agentv3

import (
	"regexp"
	"sort"
	"strings"

	"csust-got/config"
	"csust-got/util"

	"go.uber.org/zap"
)

const (
	defaultOutputFormat = "markdown"
	outputFormatHTML    = "html"
)

// extractReasonPatt matches <think>...</think> blocks at the start of output.
var extractReasonPatt = regexp.MustCompile(`(?si)^\s*<think>\s*(?P<reason>.*?)(?:\s*</think>|$)\s*`)
var reasonGroup = extractReasonPatt.SubexpIndex("reason")

// FormatOutputWithReason formats output text with reasoning content according to config.
// Handles both native reasoning (from model protocol) and parsed <think> tags.
func FormatOutputWithReason(text string, nativeReason string, format *config.AgentOutputConfig) string {
	return formatOutputParts(splitOutputWithReason(text, nativeReason, format), format)
}

func formatOutputParts(parts outputParts, format *config.AgentOutputConfig) string {
	buf := strings.Builder{}

	outputFormat := format.GetFormat()
	if outputFormat == "" {
		zap.L().Warn("agentv3: text output format empty, defaulting to markdown")
		outputFormat = defaultOutputFormat
	}

	if parts.reason != "" {
		reasonFormat := format.GetReasonFormat()
		if reasonFormat == "" {
			zap.L().Warn("agentv3: reason format empty, defaulting to none")
			reasonFormat = "none"
		}
		switch reasonFormat {
		case "quote":
			formatText(&buf, parts.reason, outputFormat, wholeTextTypeQuote)
		case "collapse":
			formatText(&buf, parts.reason, outputFormat, wholeTextTypeCollapse)
		default:
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
	}

	payloadFormat := format.GetPayloadFormat()
	if payloadFormat == "" {
		zap.L().Warn("agentv3: payload format empty, defaulting to plain")
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

	formatText(&buf, parts.payload, outputFormat, payloadType)
	return buf.String()
}

type outputParts struct {
	reason  string
	payload string
}

func splitOutputWithReason(text string, nativeReason string, format *config.AgentOutputConfig) outputParts {
	if format.GetUseNativeReasoning() {
		return outputParts{reason: nativeReason, payload: text}
	}

	matches := extractReasonPatt.FindStringSubmatchIndex(text)
	if len(matches) == 0 {
		return outputParts{payload: text}
	}

	reasonStart := matches[reasonGroup*2]
	reasonEnd := matches[reasonGroup*2+1]
	return outputParts{
		reason:  text[reasonStart:reasonEnd],
		payload: text[matches[1]:],
	}
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
	if strings.TrimSpace(text) == "" {
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
	case outputFormatHTML:
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
func GetParseMode(format *config.AgentOutputConfig) string {
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

const telegramMessageLimit = util.TelegramMessageLimit

type telegramChunk struct {
	formatted string
	raw       string
}

// formatTelegramChunks formats text and reasoning into messages that each fit within limit UTF-16 units.
// Every chunk is formatted from its own raw slice so escaping and wrappers stay balanced per message.
func formatTelegramChunks(text string, nativeReason string, format *config.AgentOutputConfig, limit int) []telegramChunk {
	parts := splitOutputWithReason(text, nativeReason, format)
	whole := formatOutputParts(parts, format)
	if whole == "" {
		return nil
	}
	fits := func(p outputParts) bool { return util.UTF16Len(formatOutputParts(p, format)) <= limit }
	if fits(parts) {
		return []telegramChunk{{formatted: whole, raw: rawOutputParts(parts, format)}}
	}

	var chunks []telegramChunk
	emit := func(p outputParts) {
		if formatted := formatOutputParts(p, format); formatted != "" {
			chunks = append(chunks, telegramChunk{formatted: formatted, raw: rawOutputParts(p, format)})
		}
	}
	head := parts.reason
	if head != "" && !reasonLeavesRoom(parts, fits) {
		rest := head
		for rest != "" {
			var chunk string
			chunk, rest = takeTelegramChunk(rest, func(c string) bool { return fits(outputParts{reason: c}) })
			emit(outputParts{reason: chunk})
		}
		head = ""
	}
	rest := parts.payload
	for rest != "" {
		var chunk string
		chunk, rest = takeTelegramChunk(rest, func(c string) bool { return fits(outputParts{reason: head, payload: c}) })
		emit(outputParts{reason: head, payload: chunk})
		head = ""
	}
	if head != "" {
		emit(outputParts{reason: head})
	}
	return chunks
}

func reasonLeavesRoom(parts outputParts, fits func(outputParts) bool) bool {
	if parts.payload == "" {
		return fits(outputParts{reason: parts.reason})
	}
	firstRune := parts.payload
	for i := range parts.payload {
		if i > 0 {
			firstRune = parts.payload[:i]
			break
		}
	}
	return fits(outputParts{reason: parts.reason, payload: firstRune})
}

// rawOutputParts returns the unformatted text a chunk carries, for the plain-text retry path.
func rawOutputParts(parts outputParts, format *config.AgentOutputConfig) string {
	reason := parts.reason
	switch wholeTextType(format.GetReasonFormat()) {
	case wholeTextTypeQuote, wholeTextTypeCollapse:
	default:
		reason = ""
	}
	switch {
	case strings.TrimSpace(reason) == "":
		return parts.payload
	case strings.TrimSpace(parts.payload) == "":
		return reason
	default:
		return reason + "\n\n" + parts.payload
	}
}

// chunkPlainTelegramText splits unformatted text into pieces of at most limit UTF-16 units.
func chunkPlainTelegramText(text string, limit int) []string {
	return chunkTextBy(text, func(c string) bool { return util.UTF16Len(c) <= limit })
}

// chunkTextBy splits text into consecutive pieces that each satisfy fits.
func chunkTextBy(text string, fits func(string) bool) []string {
	var chunks []string
	rest := text
	for rest != "" {
		var chunk string
		chunk, rest = takeTelegramChunk(rest, fits)
		chunks = append(chunks, chunk)
	}
	return chunks
}

type textBoundary struct {
	pos       int
	paragraph bool
	inFence   bool
}

// takeTelegramChunk returns the longest prefix of text accepted by fits and the remaining text.
// It prefers paragraph breaks, then line breaks, skips boundaries inside code fences and
// avoids boundaries that would leave the chunk under half full, falling back to a split between runes.
func takeTelegramChunk(text string, fits func(string) bool) (string, string) {
	if text == "" {
		return "", ""
	}
	if fits(text) {
		return text, ""
	}
	runes := []rune(text)
	hard := sort.Search(len(runes), func(i int) bool { return !fits(string(runes[:i+1])) })
	if hard == 0 {
		hard = 1
	}
	hardCut := len(string(runes[:hard]))
	bounds, fenceAtCut := scanTextBoundaries(text, hardCut)
	minCut := hardCut / 2
	pick := func(accept func(textBoundary) bool) (int, bool) {
		for i := len(bounds) - 1; i >= 0; i-- {
			if b := bounds[i]; accept(b) && fits(text[:b.pos]) {
				return b.pos, true
			}
		}
		return 0, false
	}
	for _, paragraph := range []bool{true, false} {
		if cut, ok := pick(func(b textBoundary) bool { return b.paragraph == paragraph && !b.inFence && b.pos >= minCut }); ok {
			return trimChunk(text, cut), text[cut:]
		}
	}
	if fenceAtCut {
		for _, paragraph := range []bool{true, false} {
			if cut, ok := pick(func(b textBoundary) bool { return b.paragraph == paragraph && !b.inFence }); ok {
				return trimChunk(text, cut), text[cut:]
			}
		}
	}
	for _, paragraph := range []bool{true, false} {
		if cut, ok := pick(func(b textBoundary) bool { return b.paragraph == paragraph && b.pos >= minCut }); ok {
			return trimChunk(text, cut), text[cut:]
		}
	}
	return trimChunk(text, hardCut), text[hardCut:]
}

func trimChunk(text string, cut int) string {
	if trimmed := strings.TrimRight(text[:cut], "\n"); trimmed != "" {
		return trimmed
	}
	return text[:cut]
}

// scanTextBoundaries lists the line and paragraph breaks ending at or before limit,
// flagging those inside a ``` fence, and reports whether limit itself falls inside a fence.
func scanTextBoundaries(text string, limit int) ([]textBoundary, bool) {
	var bounds []textBoundary
	inFence := false
	lineStart := 0
	for i := 0; i < len(text); {
		idx := strings.Index(text[i:], "\n")
		if idx < 0 || i+idx >= limit {
			break
		}
		lineEnd := i + idx
		if isFenceLine(text[lineStart:lineEnd]) {
			inFence = !inFence
		}
		paragraph := strings.HasPrefix(text[lineEnd:], "\n\n")
		pos := lineEnd + 1
		if paragraph {
			pos = lineEnd + 2
		}
		if pos <= limit && pos < len(text) {
			bounds = append(bounds, textBoundary{pos: pos, paragraph: paragraph, inFence: inFence})
		}
		i = lineEnd + 1
		lineStart = i
	}
	return bounds, inFence
}

func isFenceLine(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "```")
}

// tailTelegramPreview returns a formatted preview that fits within limit, showing only the tail of text when needed.
func tailTelegramPreview(text string, nativeReason string, format *config.AgentOutputConfig, limit int) telegramChunk {
	parts := splitOutputWithReason(text, nativeReason, format)
	formatted := formatOutputParts(parts, format)
	if util.UTF16Len(formatted) <= limit {
		return telegramChunk{formatted: formatted, raw: rawOutputParts(parts, format)}
	}
	runes := []rune(parts.payload)
	const ellipsis = "…"
	tail := func(n int) outputParts {
		return outputParts{payload: ellipsis + string(runes[len(runes)-n:])}
	}
	n := sort.Search(len(runes), func(i int) bool {
		return util.UTF16Len(formatOutputParts(tail(i+1), format)) > limit
	})
	if n == 0 {
		return telegramChunk{}
	}
	return telegramChunk{formatted: formatOutputParts(tail(n), format), raw: rawOutputParts(tail(n), format)}
}
