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

	"github.com/meilisearch/meilisearch-go"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

type resultMsg struct {
	Text    string `json:"text"`
	Caption string `json:"caption,omitempty"`
	Id      int64  `json:"message_id"`
	From    struct {
		LastName  string `json:"last_name"`
		FirstName string `json:"first_name"`
	} `json:"from"`
	Formatted map[string]any `json:"_formatted,omitempty"`
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
		if message.Text == "" && message.Caption != "" {
			// If text is empty, use caption as text
			result[i]["text"] = message.Caption
		}
		if message.Formatted != nil {
			// If _formatted field exists, use it to extract text
			if formattedText, ok := message.Formatted["text"].(string); ok && formattedText != "" {
				result[i]["text"] = formattedText
			}
			if formattedCaption, ok := message.Formatted["caption"].(string); ok && formattedCaption != "" {
				result[i]["text"] = formattedCaption
			}
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
		switch option {
		case "-id":
			// when search by id, index 0 arg is "-id", 1 arg is id, pass rest to query
			var err error
			chatId, err = strconv.ParseInt(command.Arg(1), 10, 64)
			if err != nil {
				log.Error("[MeiliSearch]: Parse chat id failed", zap.String("Search args", command.ArgAllInOneFrom(0)), zap.Error(err))
				return "Invalid chat id"
			}
			searchKeywordIdx = 2
		case "-p":
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
			HitsPerPage:           10,
			Page:                  page,
			Filter:                "text NOT STARTS WITH '/' AND caption NOT STARTS WITH '/'", // Filter out command messages
			RankingScoreThreshold: 0.4,                                                        // Set a threshold for ranking score
			AttributesToSearchOn:  []string{"text", "caption"},                                // Search in text and caption fields
			AttributesToCrop:      []string{"text", "caption"},                                // Crop text field
			CropLength:            30,                                                         // Crop length for text
			CropMarker:            "...",
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
		rplMsg += fmt.Sprintf("消息[%s](%s%s): `%s` \n\n",
			respMap[item]["id"], chatUrl, respMap[item]["id"],
			util.EscapeTgMDv2ReservedChars(respMap[item]["text"]))
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
