package inline

import (
	"csust-got/config"

	tb "gopkg.in/tucnak/telebot.v3"
)

func QueryLocation(ctx tb.Context) error {
	q := ctx.Query()
	bot := config.BotConfig.Bot
	// lat, lon := q.Location.Lat, q.Location.Lng
	query := q.Text

	// msg := fmt.Sprintf("%s\nYour Location:\n  Lat: %f Lon: %f\n", getLocationFromLatLon(lat, lon), lat, lon)

	results := tb.Results{
		&tb.ResultBase{
			Content: &tb.InputTextMessageContent{
				Text: query,
			},
		},
	}

	bot.Answer(q, &tb.QueryResponse{
		Results:   results,
		CacheTime: 60,
	})
	return nil
}

func getLocationFromLatLon(lat, lon float32) string {
	return ""
}
