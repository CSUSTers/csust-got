package prom

import (
	"context"
	"strconv"
	"strings"
	"time"

	"csust-got/config"

	"github.com/prometheus/common/model"
)

// MsgCount model of count message.
type MsgCount struct {
	Name  string
	Value int
}

// QueryMessageCount query message count of prometheus.
func QueryMessageCount(chat string) ([]MsgCount, error) {
	query := config.BotConfig.PromConfig.MessageQuery
	query = strings.ReplaceAll(query, "$group", chat)
	return ExecQuery(query, time.Now())
}

// QueryStickerCount query sticker count of prometheus.
func QueryStickerCount(chat string) ([]MsgCount, error) {
	query := config.BotConfig.PromConfig.StickerQuery
	query = strings.ReplaceAll(query, "$group", chat)
	return ExecQuery(query, time.Now())
}

// ExecQuery exec query and get data.
func ExecQuery(query string, ts time.Time) ([]MsgCount, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, _, err := client.Query(ctx, query, ts)
	if err != nil {
		return nil, err
	}
	vec := value.(model.Vector)
	res := make([]MsgCount, 0)
	for _, v := range vec {
		name := v.Metric.String()
		cnt, _ := strconv.ParseFloat(v.Value.String(), 64)
		res = append(res, MsgCount{
			Name:  name[11 : len(name)-2],
			Value: int(cnt) + 1,
		})
	}
	return res, err
}
