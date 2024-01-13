package store

import (
	"csust-got/log"
	"csust-got/orm"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
	"time"
)

type DeleteMsgQueue struct {
	bot       *Bot
	queueName string
}

func (q *DeleteMsgQueue) Push(m *Message, delAt time.Time) error {
	return orm.PushQueue(q.queueName, m, delAt.Unix())
}

func (q *DeleteMsgQueue) Cancel(m *Message) error {
	return orm.RemoveFromQueue(q.queueName, m)
}

func (q *DeleteMsgQueue) fetch() ([]*Message, error) {
	msgs, err := orm.PopQueue(q.queueName, 0, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	var ms []*Message
	for _, msg := range msgs {
		m, ok := msg.(*Message)
		if !ok {
			continue
		}
		ms = append(ms, m)
	}
	return ms, nil
}

func (q *DeleteMsgQueue) process(m *Message) error {
	// delete message
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
