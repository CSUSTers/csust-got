package agentv3

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf16"

	tb "gopkg.in/telebot.v3"
)

// getMessageTextWithEntities reconstructs the formatted text from a Telegram message
// using its entities to preserve links and other formatting that would be lost in raw Text field.
//
// This function solves the issue where chat AI models couldn't access URLs from formatted links.
// When users send messages like [title](url) or <a href="url">title</a>, Telegram stores:
// - Text field: only the visible text ("title")
// - Entities field: formatting info including the actual URL
//
// This function reconstructs the original formatted text by combining both fields.
// It returns markdown-formatted text by default, or HTML if htmlFormat is true.
func getMessageTextWithEntities(msg *tb.Message, htmlFormat bool) string {
	if msg == nil {
		return ""
	}

	// Get the raw text - prefer Text over Caption
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}

	// If no entities, return the raw text
	if len(entities) == 0 {
		return text
	}

	runes := []rune(text)
	normalized := normalizeEntities(runes, entities)
	if len(normalized) == 0 {
		return text
	}

	starts := make(map[int][]normalizedEntity)
	ends := make(map[int][]normalizedEntity)
	for _, entity := range normalized {
		starts[entity.start] = append(starts[entity.start], entity)
		ends[entity.end] = append(ends[entity.end], entity)
	}
	for _, entitiesAtOffset := range starts {
		sort.SliceStable(entitiesAtOffset, func(i, j int) bool {
			if entitiesAtOffset[i].end != entitiesAtOffset[j].end {
				return entitiesAtOffset[i].end > entitiesAtOffset[j].end
			}
			return entitiesAtOffset[i].index < entitiesAtOffset[j].index
		})
	}
	for _, entitiesAtOffset := range ends {
		sort.SliceStable(entitiesAtOffset, func(i, j int) bool {
			if entitiesAtOffset[i].start != entitiesAtOffset[j].start {
				return entitiesAtOffset[i].start > entitiesAtOffset[j].start
			}
			return entitiesAtOffset[i].index > entitiesAtOffset[j].index
		})
	}

	var result strings.Builder
	for offset := range len(runes) + 1 {
		for _, entity := range ends[offset] {
			writeEntityClose(&result, entity, htmlFormat)
		}
		for _, entity := range starts[offset] {
			writeEntityOpen(&result, entity, htmlFormat)
		}
		if offset < len(runes) {
			text := string(runes[offset])
			if htmlFormat {
				text = html.EscapeString(text)
			}
			result.WriteString(text)
		}
	}

	return result.String()
}

type normalizedEntity struct {
	entity tb.MessageEntity
	start  int
	end    int
	index  int
	text   string
}

func normalizeEntities(runes []rune, entities []tb.MessageEntity) []normalizedEntity {
	normalized := make([]normalizedEntity, 0, len(entities))
	for index, entity := range entities {
		start := utf16OffsetToRuneIndex(runes, entity.Offset)
		end := utf16OffsetToRuneIndex(runes, entity.Offset+entity.Length)
		if start >= end {
			continue
		}
		normalized = append(normalized, normalizedEntity{
			entity: entity,
			start:  start,
			end:    end,
			index:  index,
			text:   string(runes[start:end]),
		})
	}
	return normalized
}

func utf16OffsetToRuneIndex(runes []rune, offset int) int {
	if offset <= 0 {
		return 0
	}

	units := 0
	for index, r := range runes {
		next := units + utf16.RuneLen(r)
		if offset < next {
			return index
		}
		if offset == next {
			return index + 1
		}
		units = next
	}
	return len(runes)
}

func writeEntityOpen(buf *strings.Builder, normalized normalizedEntity, htmlFormat bool) {
	entity := normalized.entity
	if htmlFormat {
		switch entity.Type {
		case tb.EntityTextLink:
			fmt.Fprintf(buf, `<a href="%s">`, html.EscapeString(entity.URL))
		case tb.EntityBold:
			buf.WriteString("<b>")
		case tb.EntityItalic:
			buf.WriteString("<i>")
		case tb.EntityCode:
			buf.WriteString("<code>")
		case tb.EntityUnderline:
			buf.WriteString("<u>")
		case tb.EntityStrikethrough:
			buf.WriteString("<s>")
		case tb.EntitySpoiler:
			buf.WriteString(`<span class="tg-spoiler">`)
		case tb.EntityCodeBlock:
			if entity.Language != "" {
				fmt.Fprintf(buf, `<pre><code class="language-%s">`, html.EscapeString(entity.Language))
			} else {
				buf.WriteString("<pre>")
			}
		case tb.EntityBlockquote:
			buf.WriteString("<blockquote>")
		case tb.EntityMention:
			fmt.Fprintf(buf, `<a href="tg:%s">`, html.EscapeString(strings.TrimPrefix(normalized.text, "@")))
		case tb.EntityTMention:
			if entity.User != nil {
				fmt.Fprintf(buf, `<a href="tg:user?id=%d">`, entity.User.ID)
			}
		default:
		}
		return
	}

	switch entity.Type {
	case tb.EntityTextLink, tb.EntityMention:
		buf.WriteByte('[')
	case tb.EntityTMention:
		if entity.User != nil {
			buf.WriteByte('[')
		}
	case tb.EntityBold:
		buf.WriteString("**")
	case tb.EntityItalic:
		buf.WriteByte('*')
	case tb.EntityCode:
		buf.WriteByte('`')
	case tb.EntityUnderline:
		buf.WriteString("__")
	case tb.EntityStrikethrough:
		buf.WriteString("~~")
	case tb.EntitySpoiler:
		buf.WriteString("||")
	case tb.EntityCodeBlock:
		if entity.Language != "" {
			fmt.Fprintf(buf, "```%s\n", entity.Language)
		} else {
			buf.WriteString("```\n")
		}
	case tb.EntityBlockquote:
		buf.WriteString("> ")
	default:
	}
}

func writeEntityClose(buf *strings.Builder, normalized normalizedEntity, htmlFormat bool) {
	entity := normalized.entity
	if htmlFormat {
		switch entity.Type {
		case tb.EntityTextLink, tb.EntityMention:
			buf.WriteString("</a>")
		case tb.EntityTMention:
			if entity.User != nil {
				buf.WriteString("</a>")
			}
		case tb.EntityBold:
			buf.WriteString("</b>")
		case tb.EntityItalic:
			buf.WriteString("</i>")
		case tb.EntityCode:
			buf.WriteString("</code>")
		case tb.EntityUnderline:
			buf.WriteString("</u>")
		case tb.EntityStrikethrough:
			buf.WriteString("</s>")
		case tb.EntitySpoiler:
			buf.WriteString("</span>")
		case tb.EntityCodeBlock:
			if entity.Language != "" {
				buf.WriteString("</code></pre>")
			} else {
				buf.WriteString("</pre>")
			}
		case tb.EntityBlockquote:
			buf.WriteString("</blockquote>")
		default:
		}
		return
	}

	switch entity.Type {
	case tb.EntityTextLink:
		fmt.Fprintf(buf, "](%s)", entity.URL)
	case tb.EntityMention:
		fmt.Fprintf(buf, "](tg:%s)", strings.TrimPrefix(normalized.text, "@"))
	case tb.EntityTMention:
		if entity.User != nil {
			fmt.Fprintf(buf, "](tg:user?id=%d)", entity.User.ID)
		}
	case tb.EntityBold:
		buf.WriteString("**")
	case tb.EntityItalic:
		buf.WriteByte('*')
	case tb.EntityCode:
		buf.WriteByte('`')
	case tb.EntityUnderline:
		buf.WriteString("__")
	case tb.EntityStrikethrough:
		buf.WriteString("~~")
	case tb.EntitySpoiler:
		buf.WriteString("||")
	case tb.EntityCodeBlock:
		buf.WriteString("\n```")
	default:
	}
}

// getTextSubstring safely extracts a substring using UTF-16 offsets (like Telegram entities)
// This is needed because Telegram entities use UTF-16 code unit offsets, not byte offsets
func getTextSubstring(text string, start, end int) string {
	runes := []rune(text)
	startIndex := utf16OffsetToRuneIndex(runes, start)
	endIndex := utf16OffsetToRuneIndex(runes, end)
	if startIndex >= endIndex {
		return ""
	}
	return string(runes[startIndex:endIndex])
}
