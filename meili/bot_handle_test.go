package meili

import (
	"csust-got/entities"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/telebot.v3"
)

// Test helpers to simulate command parsing
func createTestMessage(text string) *telebot.Message {
	return &telebot.Message{
		Text: text,
	}
}

func TestPaginationCommandGeneration(t *testing.T) {
	tests := []struct {
		name           string
		commandText    string
		currentPage    int64
		totalPages     int64
		expectedPrev   string
		expectedNext   string
		shouldHavePrev bool
		shouldHaveNext bool
		usedChatId     bool
		chatId         int64
	}{
		{
			name:           "Regular search with pagination",
			commandText:    "/search hello world",
			currentPage:    2,
			totalPages:     5,
			expectedPrev:   "/search -p 1 hello world",
			expectedNext:   "/search -p 3 hello world",
			shouldHavePrev: true,
			shouldHaveNext: true,
			usedChatId:     false,
		},
		{
			name:           "Search with chat ID - middle page",
			commandText:    "/search -id -1001234567890 hello world",
			currentPage:    2,
			totalPages:     5,
			expectedPrev:   "/search -p 1 -id -1001234567890 hello world",
			expectedNext:   "/search -p 3 -id -1001234567890 hello world",
			shouldHavePrev: true,
			shouldHaveNext: true,
			usedChatId:     true,
			chatId:         -1001234567890,
		},
		{
			name:           "Search with chat ID - first page",
			commandText:    "/search -id -1001234567890 hello world",
			currentPage:    1,
			totalPages:     3,
			expectedNext:   "/search -p 2 -id -1001234567890 hello world",
			shouldHavePrev: false,
			shouldHaveNext: true,
			usedChatId:     true,
			chatId:         -1001234567890,
		},
		{
			name:           "Search with chat ID - last page",
			commandText:    "/search -id -1001234567890 hello world",
			currentPage:    3,
			totalPages:     3,
			expectedPrev:   "/search -p 2 -id -1001234567890 hello world",
			shouldHavePrev: true,
			shouldHaveNext: false,
			usedChatId:     true,
			chatId:         -1001234567890,
		},
		{
			name:           "Search with page param should preserve it",
			commandText:    "/search -p 2 hello world",
			currentPage:    2,
			totalPages:     5,
			expectedPrev:   "/search -p 1 hello world",
			expectedNext:   "/search -p 3 hello world",
			shouldHavePrev: true,
			shouldHaveNext: true,
			usedChatId:     false,
		},
		{
			name:           "Edge case: -p without original -id should not add -id",
			commandText:    "/search -p 2 hello world",
			currentPage:    2,
			totalPages:     3,
			expectedPrev:   "/search -p 1 hello world",
			expectedNext:   "/search -p 3 hello world",
			shouldHavePrev: true,
			shouldHaveNext: true,
			usedChatId:     false,
		},
		{
			name:           "Combined parameters: -id first, then -p",
			commandText:    "/search -id -1001234567890 -p 2 hello world",
			currentPage:    2,
			totalPages:     5,
			expectedPrev:   "/search -p 1 -id -1001234567890 hello world",
			expectedNext:   "/search -p 3 -id -1001234567890 hello world",
			shouldHavePrev: true,
			shouldHaveNext: true,
			usedChatId:     true,
			chatId:         -1001234567890,
		},
		{
			name:           "Combined parameters: -p first, then -id",
			commandText:    "/search -p 2 -id -1001234567890 hello world",
			currentPage:    2,
			totalPages:     5,
			expectedPrev:   "/search -p 1 -id -1001234567890 hello world",
			expectedNext:   "/search -p 3 -id -1001234567890 hello world",
			shouldHavePrev: true,
			shouldHaveNext: true,
			usedChatId:     true,
			chatId:         -1001234567890,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := createTestMessage(tt.commandText)
			command := entities.FromMessage(msg)

			// Parse the command to understand the structure - support both -id and -p parameters
			searchKeywordIdx := 0
			usedChatId := false

			// Loop through arguments to find and process -id and -p parameters
			for i := 0; i < command.Argc()-1; i++ { // -1 because we need at least one argument after each parameter
				arg := command.Arg(i)
				switch arg {
				case paramIDFlag:
					usedChatId = true
					searchKeywordIdx = i + 2
				case "-p":
					searchKeywordIdx = i + 2
				}
			}

			searchQuery := command.ArgAllInOneFrom(searchKeywordIdx)

			// Test the pagination logic
			prevCmd, nextCmd := "", ""
			if tt.shouldHavePrev {
				prevCmd = generatePaginationCommand(tt.currentPage-1, searchQuery, usedChatId, tt.chatId)
			}
			if tt.shouldHaveNext {
				nextCmd = generatePaginationCommand(tt.currentPage+1, searchQuery, usedChatId, tt.chatId)
			}

			if tt.shouldHavePrev {
				assert.Equal(t, tt.expectedPrev, prevCmd, "Previous page command should match expected")
			}
			if tt.shouldHaveNext {
				assert.Equal(t, tt.expectedNext, nextCmd, "Next page command should match expected")
			}

			// Verify the logic matches our test expectations
			assert.Equal(t, tt.usedChatId, usedChatId, "Chat ID usage detection should match")
		})
	}
}

// TestGeneratePaginationCommand tests the helper function directly
func TestGeneratePaginationCommand(t *testing.T) {
	tests := []struct {
		name           string
		page           int64
		searchQuery    string
		usedChatIdParam bool
		chatId         int64
		expected       string
	}{
		{
			name:           "Regular pagination without chat ID",
			page:           2,
			searchQuery:    "hello world",
			usedChatIdParam: false,
			chatId:         0,
			expected:       "/search -p 2 hello world",
		},
		{
			name:           "Pagination with chat ID",
			page:           3,
			searchQuery:    "test query",
			usedChatIdParam: true,
			chatId:         -1001234567890,
			expected:       "/search -p 3 -id -1001234567890 test query",
		},
		{
			name:           "First page with chat ID",
			page:           1,
			searchQuery:    "first page",
			usedChatIdParam: true,
			chatId:         -1001111111111,
			expected:       "/search -p 1 -id -1001111111111 first page",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePaginationCommand(tt.page, tt.searchQuery, tt.usedChatIdParam, tt.chatId)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCombinedParameterParsing tests parsing of both -id and -p parameters together
func TestCombinedParameterParsing(t *testing.T) {
	tests := []struct {
		name              string
		commandText       string
		expectedChatId    bool
		expectedKeywordIdx int
	}{
		{
			name:              "No parameters",
			commandText:       "/search hello world",
			expectedChatId:    false,
			expectedKeywordIdx: 0,
		},
		{
			name:              "Only -id parameter",
			commandText:       "/search -id -1001234567890 hello world",
			expectedChatId:    true,
			expectedKeywordIdx: 2,
		},
		{
			name:              "Only -p parameter",
			commandText:       "/search -p 2 hello world",
			expectedChatId:    false,
			expectedKeywordIdx: 2,
		},
		{
			name:              "Combined: -id first, then -p",
			commandText:       "/search -id -1001234567890 -p 2 hello world",
			expectedChatId:    true,
			expectedKeywordIdx: 4,
		},
		{
			name:              "Combined: -p first, then -id",
			commandText:       "/search -p 2 -id -1001234567890 hello world",
			expectedChatId:    true,
			expectedKeywordIdx: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := createTestMessage(tt.commandText)
			command := entities.FromMessage(msg)

			// Simulate the parsing logic from the main function
			searchKeywordIdx := 0
			usedChatId := false

			// Loop through arguments to find and process -id and -p parameters
			for i := 0; i < command.Argc()-1; i++ {
				arg := command.Arg(i)
				switch arg {
				case paramIDFlag:
					usedChatId = true
					searchKeywordIdx = i + 2
				case "-p":
					searchKeywordIdx = i + 2
				}
			}

			assert.Equal(t, tt.expectedChatId, usedChatId, "Chat ID detection should match expected")
			assert.Equal(t, tt.expectedKeywordIdx, searchKeywordIdx, "Search keyword index should match expected")
		})
	}
}