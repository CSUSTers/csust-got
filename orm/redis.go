package orm

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v7"
	"go.uber.org/zap"
)

var client *redis.Client

// InitRedis init redis
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

// LoadWhiteList load white list
func LoadWhiteList() {
	chats := util.StringsToInts(loadSpecialList("white_list"))
	log.Info("White List has load.", zap.Int("length", len(chats)))
	config.BotConfig.WhiteListConfig.Chats = chats
}

// LoadBlackList load black list
func LoadBlackList() {
	chats := util.StringsToInts(loadSpecialList("black_list"))
	log.Info("Black List has load.", zap.Int("length", len(chats)))
	config.BotConfig.BlackListConfig.Chats = chats
}

// IsNoStickerMode check group in NoSticker mode
func IsNoStickerMode(chatID int64) bool {
	ok, err := GetBool(wrapKeyWithChat("no_sticker", chatID))
	if err != nil {
		log.Error("get NoStickerMode failed", zap.Int64("chatID", chatID), zap.Error(err))
		return false
	}
	return ok
}

//ToggleNoStickerMode toggle NoSticker mode
func ToggleNoStickerMode(chatID int64) bool {
	err := ToggleBool(wrapKeyWithChat("no_sticker", chatID))
	if err != nil {
		log.Error("get NoStickerMode failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
	return err == nil
}

// Shutdown shutdown bot
func Shutdown(chatID int64) {
	err := WriteBool(wrapKeyWithChat("shutdown", chatID), true, 0)
	if err != nil {
		log.Error("Shutdown failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
}

// Boot boot bot
func Boot(chatID int64) {
	err := WriteBool(wrapKeyWithChat("shutdown", chatID), false, 0)
	if err != nil {
		log.Error("boot failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
}

// IsShutdown check bot is shutdown
func IsShutdown(chatID int64) bool {
	ok, err := GetBool(wrapKeyWithChat("shutdown", chatID))
	if err != nil {
		log.Error("get Shutdown failed", zap.Int64("chatID", chatID), zap.Error(err))
		return false
	}
	return ok
}

// IsFakeBanInCD check fake ban is in cd
func IsFakeBanInCD(chatID int64, userID int) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("get IsFakeBanInCD failed", zap.Int64("chatID", chatID), zap.Int("userID", userID), zap.Error(err))
		return true
	}
	return ok
}

// IsBanned check some one is banned
func IsBanned(chatID int64, userID int) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("get IsBanned failed", zap.Int64("chatID", chatID), zap.Int("userID", userID), zap.Error(err))
		return false
	}
	return ok
}

// GetBannedDuration get some one banned duration
func GetBannedDuration(chatID int64, userID int) time.Duration {
	sec, err := GetTTL(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("GetBannedDuration failed", zap.Int64("chatID", chatID), zap.Int("userID", userID), zap.Error(err))
	}
	return sec
}

// GetBannerDuration get fake ban cd
func GetBannerDuration(chatID int64, userID int) time.Duration {
	sec, err := GetTTL(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("GetBannerDuration failed", zap.Int64("chatID", chatID), zap.Int("userID", userID), zap.Error(err))
	}
	return sec
}

// ResetBannedDuration reset banned duration
func ResetBannedDuration(chatID int64, bannedID int, d time.Duration) bool {
	ok, err := client.Expire(wrapKeyWithChatMember("banned", chatID, bannedID), d).Result()
	if err != nil {
		log.Error("ResetBannedDuration failed", zap.Int64("chatID", chatID), zap.Int("userID", bannedID), zap.Error(err))
		return false
	}
	return ok
}

// AddBanDuration add ban duration
func AddBanDuration(chatID int64, bannerID, bannedID int, ad time.Duration) bool {
	MakeBannerCD(chatID, bannerID, util.GetBanCD(ad))
	d := GetBannedDuration(chatID, bannedID)
	return d != 0 && ResetBannedDuration(chatID, bannedID, ad+d)
}

// Ban ban some one
func Ban(chatID int64, bannerID, bannedID int, d time.Duration) bool {
	MakeBannerCD(chatID, bannerID, util.GetBanCD(d))
	err := WriteBool(wrapKeyWithChatMember("banned", chatID, bannedID), true, d)
	if err != nil {
		log.Error("Ban failed", zap.Int64("chatID", chatID), zap.Int("userID", bannedID), zap.Error(err))
		return false
	}
	return true
}

// MakeBannerCD make banner in cd
func MakeBannerCD(chatID int64, bannerID int, d time.Duration) bool {
	err := WriteBool(wrapKeyWithChatMember("banner", chatID, bannerID), true, d)
	if err != nil {
		log.Error("Ban set CD failed", zap.Int64("chatID", chatID), zap.Int("userID", bannerID), zap.Error(err))
		return false
	}
	return true
}

// StoreHitokoto store hitokoto
func StoreHitokoto(hitokoto string) {
	err := client.SAdd(wrapKey("hitokoto"), hitokoto).Err()
	if err != nil {
		log.Error("save hitokoto to redis failed", zap.Error(err))
	}
}

// GetHitokoto get hitokoto
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

// RegisterYiban register yiban
func RegisterYiban(userID int, tel string) bool {
	err := client.HSet(wrapKey("yiban"), userID, tel).Err()
	if err != nil {
		log.Error("save yiban to redis failed", zap.Error(err))
		return false
	}
	return true
}

// GetYiban get yiban status
func GetYiban(userID int) string {
	tel, err := client.HGet(wrapKey("yiban"), strconv.Itoa(userID)).Result()
	if err != nil && err != redis.Nil {
		log.Error("get yiban from redis failed", zap.Error(err))
		return ""
	}
	return tel
}

// GetAllYiban get all yiban status
func GetAllYiban() map[int]string {
	res := make(map[int]string)
	mp, err := client.HGetAll(wrapKey("yiban")).Result()
	if err != nil && err != redis.Nil {
		log.Error("get all yiban from redis failed", zap.Error(err))
		return res
	}
	for k, v := range mp {
		ki, _ := strconv.Atoi(k)
		res[ki] = v
	}
	return res
}

// DelYiban del user from yiban
func DelYiban(userID int) bool {
	err := client.HDel(wrapKey("yiban"), strconv.Itoa(userID)).Err()
	if err != nil && err != redis.Nil {
		log.Error("delete yiban from redis failed", zap.Error(err))
		return false
	}
	return true
}

// YibanNotified mark user is notified
func YibanNotified(userID int) {
	now := time.Now().In(util.TimeZoneCST)
	d := time.Duration(24*60-now.Hour()*60-now.Minute()) * time.Minute
	err := WriteBool(wrapKeyWithUser("yiban_notified", userID), true, d)
	if err != nil {
		log.Error("Set yiban notified failed", zap.Int("userID", userID), zap.Error(err))
	}
}

// IsYibanNotified check user is notified
func IsYibanNotified(userID int) bool {
	ok, err := GetBool(wrapKeyWithUser("yiban_notified", userID))
	if err != nil {
		log.Error("Get yiban notified failed", zap.Int("userID", userID), zap.Error(err))
	}
	return ok
}

// YibanFailedNotified mark yiban notidied failed
func YibanFailedNotified(userID int) {
	now := time.Now().In(util.TimeZoneCST)
	d := time.Duration(24*60-now.Hour()*60-now.Minute()) * time.Minute
	err := WriteBool(wrapKeyWithUser("yiban_failed_notified", userID), true, d)
	if err != nil {
		log.Error("Set yiban failed notified failed", zap.Int("userID", userID), zap.Error(err))
	}
}

// IsYibanFailedNotified check user notified failed
func IsYibanFailedNotified(userID int) bool {
	ok, err := GetBool(wrapKeyWithUser("yiban_failed_notified", userID))
	if err != nil {
		log.Error("Get yiban failed notified failed", zap.Int("userID", userID), zap.Error(err))
	}
	return ok
}
