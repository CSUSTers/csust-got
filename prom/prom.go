package prom

import (
	"csust-got/command"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

func init() {
	prometheus.MustRegister(commandTimes)
	prometheus.MustRegister(messageCount)
	prometheus.MustRegister(updateCostTime)

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe(":8080", nil)
		if err != nil {
			zap.L().Error(err.Error())
		}
	}()
}

// DailUpdate - dail an update
func DailUpdate(update tgbotapi.Update, costTime time.Duration) {
	message := update.Message
	if message == nil {
		return
	}

	chat := message.Chat
	if chat.IsPrivate() {
		// ignore private chat
		return
	}
	updateCostTime.WithLabelValues(chat.Title).Set(float64(costTime.Nanoseconds()) / 1e6)

	user := message.From
	if user == nil || user.IsBot {
		return
	}
	username := user.UserName
	if username == "" {
		username = user.FirstName
	}

	isCommand, isSticker := "false", "false"

	if message.Sticker != nil {
		isSticker = "true"
	}

	command, _ := command.FromMessage(message)
	if command != nil {
		isCommand = "true"
		commandTimes.WithLabelValues(chat.Title, username, command.Name()).Inc()
	}

	messageCount.WithLabelValues(chat.Title, username, isCommand, isSticker).Inc()
}
