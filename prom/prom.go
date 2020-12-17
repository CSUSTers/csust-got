package prom

import (
	"csust-got/entities"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

var host, _ = os.Hostname()

func InitPrometheus() {
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

func newLabels(base, labels prometheus.Labels) prometheus.Labels {
	for k, v := range base {
		labels[k] = v
	}
	return labels
}

// DailUpdate - dail an update
func DailUpdate(update tgbotapi.Update, valied bool, costTime time.Duration) {
	if !valied {
		return
	}
	labels := prometheus.Labels{"host": host}

	message := update.Message
	if message == nil {
		return
	}

	chat := message.Chat
	labels["chat_name"] = chat.Title
	if chat.IsPrivate() {
		// ignore private chat
		return
	}

	user := message.From
	if user == nil || user.IsBot {
		return
	}
	username := user.UserName
	if username == "" {
		username = user.FirstName
	}
	labels["username"] = username

	isCommand, isSticker := "false", "false"

	if message.Sticker != nil {
		isSticker = "true"
	}

	command, _ := entities.FromMessage(message)
	if command != nil {
		isCommand = "true"
		commandTimes.With(newLabels(labels, prometheus.Labels{
			"command_name": command.Name(),
		})).Inc()
	}

	updateCostTime.With(labels).Set(float64(costTime.Nanoseconds()) / 1e6)

	messageCount.With(newLabels(labels, prometheus.Labels{
		"is_command": isCommand,
		"is_sticker": isSticker,
	})).Inc()
}
