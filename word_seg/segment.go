package wordSeg

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"github.com/huichen/sego"
	"go.uber.org/zap"
	"strconv"
	"sync"
)

// TextInput is a struct for word segment
type TextInput struct {
	text   string
	chatId int64
}

var (

	// JiebaChan is a channel for word segment
	JiebaChan      = make(chan TextInput, 100)
	startJiebaOnce = sync.Once{}
)

// InitWordSeg init word segment
func InitWordSeg() {
	startJiebaOnce.Do(func() {
		go SegWorker()
	})
}

// SegWorker is a worker for word segment
func SegWorker() {
	var seg sego.Segmenter
	seg.LoadDictionary("./dictionary.txt")
	for {
		textInput := <-JiebaChan
		segments := seg.Segment([]byte(textInput.text))
		words := sego.SegmentsToSlice(segments, true)
		key := config.BotConfig.RedisConfig.KeyPrefix + ":word_seg" + ":" + strconv.FormatInt(textInput.chatId, 10)
		for _, word := range words {
			if len(word) > 0 && word != " " {
				err := orm.IncreaseSortedSetByOne(key, word)
				if err != nil {
					log.Error("[WordSegment] redis err: ", zap.Error(err))
				}
			}
		}
	}
}

// WordSegment cut words into pieces with JieBa
func WordSegment(text string, chatId int64) {
	log.Debug("[WordSegment] textInput input is: ", zap.String("textInput", text))
	JiebaChan <- TextInput{text: text, chatId: chatId}
}
