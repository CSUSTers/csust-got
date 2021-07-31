package inline

import (
	"csust-got/config"
	"fmt"
	tb "gopkg.in/tucnak/telebot.v2"
)

func QueryLocation(q *tb.Query) {
	bot := config.BotConfig.Bot
	lat, lon := q.Location.Lat, q.Location.Lng
	query := q.Text

	msg := fmt.Sprintf("%s\nYour Location:\n  Lat: %f Lon: %f\n", getLocationFromLatLon(lat, lon), lat, lon)

	results := tb.Results{
		&tb.ResultBase{
			Content: tb.InputTextMessageContent{
				Text: msg,
			},
		},
	}

	bot.Answer(q, &tb.QueryResponse{
		Results:   results,
		CacheTime: 60,
	})
}

func getLocationFromLatLon(lat, lon float32) string {
	return ""
}
