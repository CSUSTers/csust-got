package store

import (
	"csust-got/log"
	"csust-got/orm"
	"encoding/json"
	"time"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// DeleteMsgQueue is a queue to delete message
type DeleteMsgQueue struct {
	bot       *Bot
	queueName string
}

// Push a message to delete queue
func (q *DeleteMsgQueue) Push(m *Message, delAt time.Time) error {
	return orm.PushQueue(q.queueName, m, delAt.Unix())
}

// Cancel remove a message from delete queue
func (q *DeleteMsgQueue) Cancel(m *Message) error {
	return orm.RemoveFromQueue(q.queueName, m)
}

func (q *DeleteMsgQueue) fetch() ([]*Message, error) {
	msgs, err := orm.PopQueue(q.queueName, 0, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	ms := make([]*Message, 0, len(msgs))
	for _, msg := range msgs {
		m := new(Message)
		err := json.Unmarshal([]byte(msg), m)
		if err != nil {
			log.Error("unmarshal message error", zap.Error(err))
			continue
		}
		ms = append(ms, m)
	}
	return ms, nil
}

func (q *DeleteMsgQueue) process(m *Message) error {
	// delete message
	log.Info("delete message by byeWorld", zap.Int64("chat_id", m.Chat.ID), zap.Int("message_id", m.ID))
	return q.bot.Delete(m)
}

func (q *DeleteMsgQueue) init() error {
	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			msgs, err := q.fetch()
			if err != nil {
				log.Error("fetch delete msg queue error", zap.String("queue", q.queueName), zap.Error(err))
				continue
			}
			for _, msg := range msgs {
				err := q.process(msg)
				if err != nil {
					log.Error("process delete msg queue error", zap.String("queue", q.queueName), zap.Error(err))
				}
			}
		}
	}()
	return nil
}

// NewDeleteMsgQueue creates a new delete msg queue
func NewDeleteMsgQueue(queueName string, bot *Bot) *DeleteMsgQueue {
	q := &DeleteMsgQueue{
		bot:       bot,
		queueName: queueName,
	}
	err := q.init()
	if err != nil {
		log.Fatal("init delete msg queue error", zap.String("queue", queueName), zap.Error(err))
	}
	return q
}
