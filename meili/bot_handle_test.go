package meili

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncateAndHighlight(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		searchQuery string
		maxLength   int
		expected    string
	}{
		{
			name:        "text shorter than max length",
			text:        "Hello world",
			searchQuery: "world",
			maxLength:   50,
			expected:    "Hello world",
		},
		{
			name:        "text longer than max length with query found",
			text:        "This is a very long text that contains the search term somewhere in the middle of the sentence",
			searchQuery: "search",
			maxLength:   40,
			expected:    "... contains the search term somewher...",
		},
		{
			name:        "text longer than max length with query not found",
			text:        "This is a very long text that does not contain the term somewhere in the middle of the sentence",
			searchQuery: "missing",
			maxLength:   40,
			expected:    "This is a very long text that does no...",
		},
		{
			name:        "query at the beginning",
			text:        "search term is at the beginning of this long text that needs to be truncated",
			searchQuery: "search",
			maxLength:   30,
			expected:    "search term is at the begin...",
		},
		{
			name:        "query at the end",
			text:        "This is a long text that ends with the search",
			searchQuery: "search",
			maxLength:   30,
			expected:    "...t that ends with the search",
		},
		{
			name:        "case insensitive search",
			text:        "This contains SEARCH in uppercase",
			searchQuery: "search",
			maxLength:   50,
			expected:    "This contains SEARCH in uppercase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateAndHighlight(tt.text, tt.searchQuery, tt.maxLength)
			assert.Equal(t, tt.expected, result)
			assert.LessOrEqual(t, len(result), tt.maxLength, "Result should not exceed max length")
		})
	}
}

func TestTruncateAndHighlight_EdgeCases(t *testing.T) {
	t.Run("empty text", func(t *testing.T) {
		result := truncateAndHighlight("", "search", 50)
		assert.Equal(t, "", result)
	})

	t.Run("empty search query", func(t *testing.T) {
		text := "This is a long text that should be truncated"
		result := truncateAndHighlight(text, "", 20)
		assert.Equal(t, "This is a long te...", result)
	})

	t.Run("very short max length", func(t *testing.T) {
		text := "This is text"
		result := truncateAndHighlight(text, "is", 8)
		assert.LessOrEqual(t, len(result), 8)
	})
}