package prom

import (
	"csust-got/command"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

func init() {
	prometheus.MustRegister(commandTimes)
	prometheus.MustRegister(messageCount)

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe(":8080", nil)
		if err != nil {
			zap.L().Error(err.Error())
		}
	}()
}

// DailUpdate - dail an update
func DailUpdate(update tgbotapi.Update) {
	message := update.Message
	if message == nil {
		return
	}

	chat := message.Chat
	if chat.IsPrivate() {
		// ignore private chat
		return
	}

	user := message.From
	if user == nil || user.IsBot {
		return
	}

	isCommand, isSticker := "false", "false"

	if message.Sticker != nil {
		isSticker = "true"
	}

	command, _ := command.FromMessage(message)
	if command != nil {
		isCommand = "true"
		commandTimes.WithLabelValues(chat.Title, user.UserName, command.Name()).Inc()
	}

	messageCount.WithLabelValues(chat.Title, user.UserName, isCommand, isSticker).Inc()
}
