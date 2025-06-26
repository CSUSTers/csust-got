package meili

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/meilisearch/meilisearch-go"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

type resultMsg struct {
	Text string `json:"text"`
	Id   int64  `json:"message_id"`
	From struct {
		LastName  string `json:"last_name"`
		FirstName string `json:"first_name"`
	} `json:"from"`
}

// truncateAndHighlight truncates long text and highlights search terms
func truncateAndHighlight(text, searchQuery string, maxLength int) string {
	if len(text) <= maxLength {
		return text
	}
	
	// Find the position of the search query in the text (case insensitive)
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(searchQuery)
	
	pos := strings.Index(lowerText, lowerQuery)
	if pos == -1 {
		// If query not found, just truncate from the beginning
		if len(text) > maxLength {
			return text[:maxLength-3] + "..."
		}
		return text
	}
	
	// Calculate how much context to show around the match
	queryLen := len(searchQuery)
	contextLength := (maxLength - queryLen - 6) / 2 // 6 for "..." on both sides
	
	start := pos - contextLength
	end := pos + queryLen + contextLength
	
	if start < 0 {
		start = 0
		end = maxLength - 3
	}
	if end > len(text) {
		end = len(text)
		start = end - maxLength + 3
		if start < 0 {
			start = 0
		}
	}
	
	result := ""
	if start > 0 {
		result += "..."
	}
	result += text[start:end]
	if end < len(text) {
		result += "..."
	}
	
	return result
}

// ExtractFields extract fields from search result
func ExtractFields(hits []any) ([]map[string]string, error) {
	var resultMsgs = make([]resultMsg, 0, len(hits))
	for _, hit := range hits {
		var message resultMsg
		hitBytes, err := json.Marshal(hit)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(hitBytes, &message); err != nil {
			return nil, err
		}
		resultMsgs = append(resultMsgs, message)
	}

	result := make([]map[string]string, len(resultMsgs))
	for i, message := range resultMsgs {
		result[i] = map[string]string{
			"text": message.Text,
			"name": message.From.FirstName + message.From.LastName,
			"id":   strconv.FormatInt(message.Id, 10),
		}
	}
	return result, nil
}

// SearchHandle handles search command
func SearchHandle(ctx Context) error {
	if config.BotConfig.MeiliConfig.Enabled {
		rplMsg := executeSearch(ctx)
		err := ctx.Reply(rplMsg, ModeMarkdownV2)
		return err
	}
	err := ctx.Reply("MeiliSearch is not enabled")
	return err
}

func executeSearch(ctx Context) string {
	command := entities.FromMessage(ctx.Message())
	chatId := ctx.Chat().ID
	page := int64(1) // default to page 1
	log.Debug("[GetChatMember]", zap.String("chatRecipient", ctx.Chat().Recipient()), zap.String("userRecipient", ctx.Sender().Recipient()))
	// parse option
	searchKeywordIdx := 0
	if command.Argc() >= 2 {
		option := command.Arg(0)
		if option == "-id" {
			// when search by id, index 0 arg is "-id", 1 arg is id, pass rest to query
			var err error
			chatId, err = strconv.ParseInt(command.Arg(1), 10, 64)
			if err != nil {
				log.Error("[MeiliSearch]: Parse chat id failed", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
				return "Invalid chat id"
			}
			searchKeywordIdx = 2
		} else if option == "-p" {
			// when search with page, index 0 arg is "-p", 1 arg is page, pass rest to query
			var err error
			page, err = strconv.ParseInt(command.Arg(1), 10, 64)
			if err != nil || page < 1 {
				log.Error("[MeiliSearch]: Parse page failed", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
				return "Invalid page number"
			}
			searchKeywordIdx = 2
		}
	}
	if searchKeywordIdx > 0 {
		// check if user is a member of chat_id group
		member, err := ctx.Bot().ChatMemberOf(ChatID(chatId), ctx.Sender())
		if err != nil {
			if errors.Is(err, ErrChatNotFound) {
				return "Chat not found"
			}
			log.Error("[MeiliSearch]: Error in GetChatMember", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
			return "Not sure if you are a member of the specified group"
		}
		if member.Role == Left || member.Role == Kicked {
			log.Error("[MeiliSearch]: Not a member of the specified group", zap.String("Search args", command.ArgAllInOneFrom(0)),
				zap.Int64("chatId", chatId), zap.String("user", ctx.Sender().Recipient()))
			return "Not a member of the specified group"
		}
	}
	query := &searchQuery{}
	if command.Argc() > 0 {
		searchRequest := meilisearch.SearchRequest{
			HitsPerPage: 10,
			Page:        page,
			Filter:      "text NOT LIKE '/%'", // Filter out command messages
		}
		query = &searchQuery{
			Query:         command.ArgAllInOneFrom(searchKeywordIdx),
			IndexName:     config.BotConfig.MeiliConfig.IndexPrefix + strconv.FormatInt(chatId, 10),
			SearchRequest: searchRequest,
		}
	}

	if query.Query == "" {
		helpMsg := fmt.Sprintf("search keyword is empty, use `%s <keyword>` to search\n\n", ctx.Message().Text)
		helpMsg += "Usage:\n"
		helpMsg += "• `/search <keyword>` - Search in current chat\n"
		helpMsg += "• `/search -id <chat_id> <keyword>` - Search in specific chat\n"
		helpMsg += "• `/search -p <page> <keyword>` - Search specific page\n"
		helpMsg += "• `/search -id <chat_id> -p <page> <keyword>` - Combined options\n"
		return helpMsg
	}

	result, err := SearchMeili(query)
	if err != nil {
		log.Error("[MeiliSearch]: search failed", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
		return "Search failed"
	}
	resp, ok := result.(*meilisearch.SearchResponse)
	if !ok {
		log.Error("[MeiliSearch]: Parse search response failed", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
		return "Parse search response failed"
	}
	if len(resp.Hits) == 0 {
		log.Error("[MeiliSearch]: No result found", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
		return "No result found"
	}
	log.Debug("[MeiliSearch]: Search success", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Any("result", resp.Hits))
	respMap, err := ExtractFields(resp.Hits)
	if err != nil {
		log.Error("[MeiliSearch]: Extract fields failed", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
		return "Extract fields failed"
	}
	var rplMsg string
	searchQuery := command.ArgAllInOneFrom(searchKeywordIdx)
	
	// Add pagination info header
	if resp.TotalPages > 1 {
		rplMsg += fmt.Sprintf("🔍 Search results \\(Page %d of %d, %d total\\):\n\n", page, resp.TotalPages, resp.TotalHits)
	} else {
		rplMsg += fmt.Sprintf("🔍 Search results \\(%d found\\):\n\n", resp.TotalHits)
	}
	// group id warping to url. e.g.: -1001817319583 -> 1817319583
	chatUrl := "https://t.me/c/" + strconv.FormatInt(chatId, 10)[4:] + "/"
	for item := range respMap {
		// Truncate long text and highlight search terms
		truncatedText := truncateAndHighlight(respMap[item]["text"], searchQuery, 200)
		rplMsg += "内容: " + "`" +
			util.EscapeTgMDv2ReservedChars(truncatedText) +
			"` " + "message id: [" + respMap[item]["id"] +
			"](" + chatUrl + respMap[item]["id"] + ") \n\n"
	}
	
	// Add pagination buttons if needed
	if resp.TotalPages > 1 {
		rplMsg += "\n"
		if page > 1 {
			rplMsg += "Use `/search -p " + strconv.FormatInt(page-1, 10) + " " + searchQuery + "` for previous page\n"
		}
		if page < resp.TotalPages {
			rplMsg += "Use `/search -p " + strconv.FormatInt(page+1, 10) + " " + searchQuery + "` for next page\n"
		}
	}
	// TODO: format rplMsg
	return rplMsg
}
