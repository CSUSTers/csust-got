package meili

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"encoding/json"
	"github.com/meilisearch/meilisearch-go"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
	"strconv"
	"strings"
)

type resultMsg struct {
	Text string `json:"text"`
	Id   int64  `json:"message_id"`
	From struct {
		LastName  string `json:"last_name"`
		FirstName string `json:"first_name"`
	} `json:"from"`
}

// EscapeTelegramReservedChars escape telegram reserved chars
func EscapeTelegramReservedChars(s string) string {
	reservedChars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}

	for _, char := range reservedChars {
		s = strings.ReplaceAll(s, char, "\\"+char)
	}

	return s
}

// ExtractFields extract fields from search result
func ExtractFields(hits []interface{}) ([]map[string]string, error) {
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
		rplMsg := excuteSearch(ctx)
		err := ctx.Reply(rplMsg, ModeMarkdownV2)
		return err
	}
	err := ctx.Reply("MeiliSearch is not enabled")
	return err
}

func excuteSearch(ctx Context) string {
	command := entities.FromMessage(ctx.Message())
	query := searchQuery{}
	if command.Argc() > 0 {
		searchRequest := meilisearch.SearchRequest{
			Limit: 10,
		}
		query = searchQuery{
			Query:         command.ArgAllInOneFrom(0),
			IndexName:     config.BotConfig.MeiliConfig.IndexPrefix + strconv.FormatInt(ctx.Chat().ID, 10),
			SearchRequest: searchRequest,
		}
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
	// -1001817319583 -> 1817319583
	chatUrl := "https://t.me/c/" + strconv.FormatInt(ctx.Chat().ID, 10)[4:] + "/"
	for item := range respMap {
		rplMsg += "内容: “ `" +
			EscapeTelegramReservedChars(respMap[item]["text"]) +
			"` ” message id: [" + respMap[item]["id"] +
			"](" + chatUrl + respMap[item]["id"] + ") \n\n"
	}
	// TODO: format rplMsg
	return rplMsg
}
