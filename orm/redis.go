package orm

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"github.com/go-redis/redis/v7"
	"go.uber.org/zap"
	"strconv"
	"strings"
	"time"
)

var client *redis.Client

func InitRedis() {
	client = NewClient()
}

// NewClient new redis client
func NewClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     config.BotConfig.RedisConfig.RedisAddr,
		Password: config.BotConfig.RedisConfig.RedisPass,
	})
}

// Ping can ping a redis client.
// return true if ping success.
func Ping(c *redis.Client) bool {
	_, err := c.Ping().Result()
	if err != nil {
		log.Error("ping redis failed", zap.Error(err))
		return false
	}
	return true
}

func wrapKey(key string) string {
	return config.BotConfig.RedisConfig.KeyPrefix + key
}

func wrapKeyWithChat(key string, chatID int64) string {
	cid := strconv.FormatInt(chatID, 10)
	return wrapKey(key + ":c" + cid)
}

func wrapKeyWithUser(key string, userID int) string {
	uid := strconv.Itoa(userID)
	return wrapKey(key + ":u" + uid)
}

func wrapKeyWithChatMember(key string, chatID int64, userID int) string {
	cid := strconv.FormatInt(chatID, 10)
	uid := strconv.Itoa(userID)
	return wrapKey(key + ":c" + cid + ":u" + uid)
}

func loadSpecialList(key string) []string {
	list, err := client.SMembers(wrapKey(key)).Result()
	if err != nil {
		if err != redis.Nil {
			log.Error("load special list failed", zap.String("key", key), zap.Error(err))
		}
		list = make([]string, 0)
	}
	return list
}

func LoadWhiteList() {
	chats := util.StringsToInts(loadSpecialList("white_list"))
	log.Info("White List has load.", zap.Int("length", len(chats)))
	config.BotConfig.WhiteListConfig.Chats = chats
}

func LoadBlackList() {
	chats := util.StringsToInts(loadSpecialList("black_list"))
	log.Info("Black List has load.", zap.Int("length", len(chats)))
	config.BotConfig.BlackListConfig.Chats = chats
}

func IsNoStickerMode(chatID int64) bool {
	ok, err := GetBool(wrapKeyWithChat("no_sticker", chatID))
	if err != nil {
		log.Error("get NoStickerMode failed", zap.Int64("chatID", chatID), zap.Error(err))
		return false
	}
	return ok
}

func ToggleNoStickerMode(chatID int64) bool {
	err := ToggleBool(wrapKeyWithChat("no_sticker", chatID))
	if err != nil {
		log.Error("get NoStickerMode failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
	return err == nil
}

func Shutdown(chatID int64) {
	err := WriteBool(wrapKeyWithChat("shutdown", chatID), true, 0)
	if err != nil {
		log.Error("Shutdown failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
}

func Boot(chatID int64) {
	err := WriteBool(wrapKeyWithChat("shutdown", chatID), false, 0)
	if err != nil {
		log.Error("boot failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
}

func IsShutdown(chatID int64) bool {
	ok, err := GetBool(wrapKeyWithChat("shutdown", chatID))
	if err != nil {
		log.Error("get Shutdown failed", zap.Int64("chatID", chatID), zap.Error(err))
		return false
	}
	return ok
}

func IsFakeBanInCD(chatID int64, userID int) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("get IsFakeBanInCD failed", zap.Int64("chatID", chatID), zap.Int("userID", userID), zap.Error(err))
		return true
	}
	return ok
}

func IsBanned(chatID int64, userID int) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("get IsBanned failed", zap.Int64("chatID", chatID), zap.Int("userID", userID), zap.Error(err))
		return false
	}
	return ok
}

func Ban(chatID int64, bannerID, bannedID int, d time.Duration) bool {
	conf := config.BotConfig.RestrictConfig
	err := WriteBool(wrapKeyWithChatMember("banner", chatID, bannerID), true, time.Duration(conf.FakeBanCDMinutes)*time.Minute)
	if err != nil {
		log.Error("Ban set CD failed", zap.Int64("chatID", chatID), zap.Int("userID", bannerID), zap.Error(err))
		return false
	}
	err = WriteBool(wrapKeyWithChatMember("banned", chatID, bannedID), true, d)
	if err != nil {
		log.Error("Ban failed", zap.Int64("chatID", chatID), zap.Int("userID", bannedID), zap.Error(err))
		return false
	}
	return true
}

func StoreHitokoto(hitokoto string) {
	err := client.SAdd(wrapKey("hitokoto"), hitokoto).Err()
	if err != nil {
		log.Error("save hitokoto to redis failed", zap.Error(err))
	}
}

func GetHitokoto(from bool) string {
	res, err := client.SRandMember(wrapKey("hitokoto")).Result()
	if err != nil {
		log.Error("get hitokoto from redis failed", zap.Error(err))
		return config.BotConfig.MessageConfig.HitokotoNotFound
	}
	if !from {
		res = res[:strings.LastIndex(res, " by ")+1]
	}
	return res
}
