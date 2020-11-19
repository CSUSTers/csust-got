package prom

import "github.com/prometheus/client_golang/prometheus"

// Record how many times a command has been called.
var commandTimes = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bot_command_times",
		Help: "Times of command has been called.",
	},
	[]string{"chat_name", "username", "command_name"},
)

// Record how many messages a user has send.
var messageCount = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bot_message_count",
		Help: "Times of command has been called.",
	},
	[]string{"chat_name", "username", "is_command", "is_sticker"},
)
