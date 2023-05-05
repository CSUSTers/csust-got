package word_seg

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"github.com/huichen/sego"
	"go.uber.org/zap"
	"strconv"
	"strings"
)

// WordSegment cut words into pieces with JieBa
func WordSegment(text string, chatId int64) {
	log.Debug("[WordSegment] text input is: ", zap.String("text", text))
	var seg sego.Segmenter
	seg.LoadDictionary("./dictionary.txt")
	segments := seg.Segment([]byte(text))
	words := sego.SegmentsToSlice(segments, true)
	log.Debug("[WordSegment] slices are: ", zap.String("words", strings.Join(words, ",")))
	key := config.BotConfig.RedisConfig.KeyPrefix + ":word_seg" + ":" + strconv.FormatInt(chatId, 10)
	for _, word := range words {
		if len(word) > 0 && word != " " {
			err := orm.IncreaseSortedSetByOne(key, word)
			if err != nil {
				log.Error("[WordSegment] redis err: ", zap.Error(err))
				return
			}
		}
	}
}
