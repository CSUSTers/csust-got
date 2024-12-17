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
	Text string `json:"text"`
	Id   int64  `json:"message_id"`
	From struct {
		LastName  string `json:"last_name"`
		FirstName string `json:"first_name"`
	} `json:"from"`
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
			Limit: 10,
		}
		query = &searchQuery{
			Query:         command.ArgAllInOneFrom(searchKeywordIdx),
			IndexName:     config.BotConfig.MeiliConfig.IndexPrefix + strconv.FormatInt(chatId, 10),
			SearchRequest: searchRequest,
		}
	}

	if query.Query == "" {
		return fmt.Sprintf("search keyword is empty, use `%s <keyword>` to search", ctx.Message().Text)
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
	// group id warping to url. e.g.: -1001817319583 -> 1817319583
	chatUrl := "https://t.me/c/" + strconv.FormatInt(chatId, 10)[4:] + "/"
	for item := range respMap {
		rplMsg += "内容: “ `" +
			util.EscapeTelegramReservedChars(respMap[item]["text"]) +
			"` ” message id: [" + respMap[item]["id"] +
			"](" + chatUrl + respMap[item]["id"] + ") \n\n"
	}
	// TODO: format rplMsg
	return rplMsg
}
