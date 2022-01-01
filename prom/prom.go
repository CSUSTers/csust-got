package prom

import (
	"net/http"
	"os"

	"csust-got/config"
	"csust-got/entities"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v3"
)

var host, _ = os.Hostname()
var client v1.API

// InitPrometheus init prometheus.
func InitPrometheus() {
	prometheus.MustRegister(commandTimes)
	prometheus.MustRegister(messageCount)
	prometheus.MustRegister(updateCostTime)
	prometheus.MustRegister(chatMemberCount)
	prometheus.MustRegister(newMemberCount)
	prometheus.MustRegister(logCount)

	if !config.BotConfig.PromConfig.Enabled {
		return
	}

	apiClient, err := api.NewClient(api.Config{
		Address: config.BotConfig.PromConfig.Address,
	})
	if err != nil {
		zap.L().Fatal("init prometheus client failed", zap.Error(err))
	}
	client = v1.NewAPI(apiClient)

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe(config.BotConfig.Listen, nil)
		if err != nil {
			zap.L().Error("InitPrometheus: Serve http failed", zap.Error(err))
			Log(zap.ErrorLevel.String())
		}
	}()
}

func newLabels(base, labels prometheus.Labels) prometheus.Labels {
	for k, v := range base {
		labels[k] = v
	}
	return labels
}

// DialMessage - dial an message.
func DialMessage(m *Message) {
	if m.Private() {
		return
	}
	labels := prometheus.Labels{"host": host}

	chat := m.Chat
	labels["chat_name"] = chat.Title

	user := m.Sender
	if user == nil || user.IsBot {
		return
	}
	username := user.Username
	if username == "" {
		username = user.FirstName
	}
	labels["username"] = username

	isCommand, isSticker := "false", "false"

	if m.Sticker != nil {
		isSticker = "true"
	}

	command := entities.FromMessage(m)
	if command != nil {
		isCommand = "true"
		commandTimes.With(newLabels(labels, prometheus.Labels{
			"command_name": command.Name(),
		})).Inc()
	}

	// updateCostTime.With(labels).Set(float64(costTime.Nanoseconds()) / 1e6)

	messageCount.With(newLabels(labels, prometheus.Labels{
		"is_command": isCommand,
		"is_sticker": isSticker,
	})).Inc()
}

// NewMember indicate some new member add to group.
func NewMember(chatName string) {
	newMemberCount.With(prometheus.Labels{
		"host":      host,
		"chat_name": chatName,
	}).Inc()
}

// MemberLeft indicate some member left group.
func MemberLeft(chatName string) {
	chatMemberCount.With(prometheus.Labels{
		"host":      host,
		"chat_name": chatName,
	}).Desc()
}

// GetMember get number of group member in a group.
// func GetMember(chatName string, num int) {
// 	chatMemberCount.With(prometheus.Labels{
// 		"host":      host,
// 		"chat_name": chatName,
// 	}).Set(float64(num))
// }

// Log record how many log print in specific level.
func Log(level string) {
	logCount.With(prometheus.Labels{
		"host":  host,
		"level": level,
	}).Inc()
}
