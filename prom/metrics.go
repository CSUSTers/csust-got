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
		Help: "How many messages a user has send.",
	},
	[]string{"chat_name", "username", "is_command", "is_sticker"},
)

// update process time
var updateCostTime = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "bot_update_process_time",
		Help: "updates process time.",
	},
	[]string{"chat_name"},
)
