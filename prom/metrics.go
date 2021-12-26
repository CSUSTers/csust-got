package prom

import "github.com/prometheus/client_golang/prometheus"

var baseLabels = []string{"host", "chat_name", "username"}

// Record how many times a command has been called.
var commandTimes = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bot_command_times",
		Help: "Times of command has been called.",
	},
	append([]string{"command_name"}, baseLabels...),
)

// Record how many messages a user has send.
var messageCount = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bot_message_count",
		Help: "How many messages a user has send.",
	},
	append([]string{"is_command", "is_sticker"}, baseLabels...),
)

// update process time.
var updateCostTime = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "bot_update_process_time",
		Help: "updates process time.",
	},
	baseLabels,
)

// chatMemberCount chatMemberCount.
var chatMemberCount = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "bot_chat_members_count",
		Help: "how many members in a chat",
	},
	[]string{"host", "chat_name"},
)

// newMemberCount newMemberCount.
var newMemberCount = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bot_new_members_count",
		Help: "how many new members in a chat",
	},
	[]string{"host", "chat_name"},
)

// logCount logCount.
var logCount = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bot_log_count",
		Help: "how many logs",
	},
	[]string{"host", "level"},
)
