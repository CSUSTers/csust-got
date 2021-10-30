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

func wrapKeyWithUser(key string, userID int64) string {
	uid := strconv.FormatInt(userID, 10)
	return wrapKey(key + ":u" + uid)
}

func wrapKeyWithChatMember(key string, chatID int64, userID int64) string {
	cid := strconv.FormatInt(chatID, 10)
	uid := strconv.FormatInt(userID, 10)
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
func IsFakeBanInCD(chatID int64, userID int64) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("get IsFakeBanInCD failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
		return true
	}
	return ok
}

// IsBanned check some one is banned
func IsBanned(chatID int64, userID int64) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("get IsBanned failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
		return false
	}
	return ok
}

// GetBannedDuration get some one banned duration
func GetBannedDuration(chatID int64, userID int64) time.Duration {
	sec, err := GetTTL(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("GetBannedDuration failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
	}
	return sec
}

// GetBannerDuration get fake ban cd
func GetBannerDuration(chatID int64, userID int64) time.Duration {
	sec, err := GetTTL(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("GetBannerDuration failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
	}
	return sec
}

// ResetBannedDuration reset banned duration
func ResetBannedDuration(chatID int64, bannedID int64, d time.Duration) bool {
	ok, err := client.Expire(wrapKeyWithChatMember("banned", chatID, bannedID), d).Result()
	if err != nil {
		log.Error("ResetBannedDuration failed", zap.Int64("chatID", chatID), zap.Int64("userID", bannedID), zap.Error(err))
		return false
	}
	return ok
}

// AddBanDuration add ban duration
func AddBanDuration(chatID int64, bannerID, bannedID int64, ad time.Duration) bool {
	MakeBannerCD(chatID, bannerID, util.GetBanCD(ad))
	d := GetBannedDuration(chatID, bannedID)
	return d != 0 && ResetBannedDuration(chatID, bannedID, ad+d)
}

// Ban ban some one
func Ban(chatID int64, bannerID, bannedID int64, d time.Duration) bool {
	MakeBannerCD(chatID, bannerID, util.GetBanCD(d))
	err := WriteBool(wrapKeyWithChatMember("banned", chatID, bannedID), true, d)
	if err != nil {
		log.Error("Ban failed", zap.Int64("chatID", chatID), zap.Int64("userID", bannedID), zap.Error(err))
		return false
	}
	return true
}

// MakeBannerCD make banner in cd
func MakeBannerCD(chatID int64, bannerID int64, d time.Duration) bool {
	err := WriteBool(wrapKeyWithChatMember("banner", chatID, bannerID), true, d)
	if err != nil {
		log.Error("Ban set CD failed", zap.Int64("chatID", chatID), zap.Int64("userID", bannerID), zap.Error(err))
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

// WatchStore watch apple store
func WatchStore(userID int64, stores []string) bool {
	// add user to watcher
	if !AppleWatcherRegister(userID) {
		return false
	}

	// add store to user's watching list
	userStore := make([]interface{}, len(stores))
	for i, v := range stores {
		userStore[i] = v
	}
	err := client.SAdd(wrapKeyWithUser("watch_store", userID), userStore...).Err()
	if err != nil {
		log.Error("register store to redis failed", zap.Int64("user", userID), zap.Any("store", stores), zap.Error(err))
		return false
	}

	// get all watching products of user
	products, ok := GetWatchingProducts(userID)
	if !ok {
		return false
	}

	return AppleTargetRegister(products, stores)
}

// RemoveStore not watch apple store
func RemoveStore(userID int64, stores []string) bool {
	// remove store from user's watching list
	userStore := make([]interface{}, len(stores))
	for i, v := range stores {
		userStore[i] = v
	}
	err := client.SRem(wrapKeyWithUser("watch_store", userID), userStore...).Err()
	if err != nil {
		log.Error("remove store from redis failed", zap.Int64("user", userID), zap.Any("store", stores), zap.Error(err))
		return false
	}

	// get all watching products of user
	products, ok := GetWatchingProducts(userID)
	if !ok {
		return false
	}

	return AppleTargetRemove(products, stores)
}

// WatchProduct watch apple product
func WatchProduct(userID int64, products []string) bool {
	// add user to watcher
	if !AppleWatcherRegister(userID) {
		return false
	}

	// add products to user's watching list
	userProduct := make([]interface{}, len(products))
	for i, v := range products {
		userProduct[i] = v
	}
	err := client.SAdd(wrapKeyWithUser("watch_product", userID), userProduct...).Err()
	if err != nil {
		log.Error("register product to redis failed", zap.Int64("user", userID), zap.Any("product", products), zap.Error(err))
		return false
	}

	// get all watching stores of user
	stores, ok := GetWatchingStores(userID)
	if !ok {
		return false
	}

	return AppleTargetRegister(products, stores)
}

// RemoveProduct not watch apple product
func RemoveProduct(userID int64, products []string) bool {
	// remove products from user's watching list
	userProduct := make([]interface{}, len(products))
	for i, v := range products {
		userProduct[i] = v
	}
	err := client.SRem(wrapKeyWithUser("watch_product", userID), userProduct...).Err()
	if err != nil {
		log.Error("remove product from redis failed", zap.Int64("user", userID), zap.Any("product", products), zap.Error(err))
		return false
	}

	// get all watching stores of user
	stores, ok := GetWatchingStores(userID)
	if !ok {
		return false
	}

	return AppleTargetRemove(products, stores)
}

// AppleWatcherRegister apple watcher register
func AppleWatcherRegister(userID int64) bool {
	err := client.SAdd(wrapKey("apple_watcher"), userID).Err()
	if err != nil {
		log.Error("register user to redis failed", zap.Int64("user", userID), zap.Error(err))
		return false
	}
	return true
}

// AppleTargetRegister apple product and store register
func AppleTargetRegister(products, stores []string) bool {
	if len(products) == 0 || len(stores) == 0 {
		return true
	}
	// get all targets
	targets := make([]interface{}, 0, len(products)*len(stores))
	for _, store := range stores {
		for _, product := range products {
			targets = append(targets, product+":"+store)
		}
	}

	// save to redis
	err := client.SAdd(wrapKey("apple_target"), targets...).Err()
	if err != nil {
		log.Error("register target to redis failed", zap.Any("target", targets), zap.Error(err))
		return false
	}

	return true
}

// AppleTargetRemove remove apple product and store
func AppleTargetRemove(products, stores []string) bool {
	if len(products) == 0 || len(stores) == 0 {
		return true
	}
	// get all targets
	targets := make([]interface{}, 0, len(products)*len(stores))
	for _, store := range stores {
		for _, product := range products {
			targets = append(targets, product+":"+store)
		}
	}

	// save to redis
	err := client.SRem(wrapKey("apple_target"), targets...).Err()
	if err != nil {
		log.Error("remove target from redis failed", zap.Any("target", targets), zap.Error(err))
		return false
	}

	return true
}

// GetWatchingStores get watching apple stores of user
func GetWatchingStores(userID int64) ([]string, bool) {
	stores, err := client.SMembers(wrapKeyWithUser("watch_store", userID)).Result()
	if err != nil && err != redis.Nil {
		log.Error("get stores of user from redis failed", zap.Int64("user", userID), zap.Error(err))
		return stores, false
	}
	return stores, true
}

// GetWatchingProducts get watching apple products of user
func GetWatchingProducts(userID int64) ([]string, bool) {
	products, err := client.SMembers(wrapKeyWithUser("watch_product", userID)).Result()
	if err != nil && err != redis.Nil {
		log.Error("get products of user from redis failed", zap.Int64("user", userID), zap.Error(err))
		return products, false
	}
	return products, true
}

// GetTargetList get watching apple store and product
func GetTargetList() ([]string, bool) {
	targets, err := client.SMembers(wrapKey("apple_target")).Result()
	if err != nil && err != redis.Nil {
		log.Error("get targets from redis failed", zap.Error(err))
		return targets, false
	}
	return targets, true
}

// SetProductName set apple product name
func SetProductName(product, name string) bool {
	err := client.Set(wrapKey("apple_product_name:"+product), name, 24*time.Hour).Err()
	if err != nil {
		log.Error("set apple_product_name to redis failed", zap.String("product", product), zap.Any("name", name), zap.Error(err))
		return false
	}
	return true
}

// GetProductName get apple product name
func GetProductName(product string) string {
	name, err := client.Get(wrapKey("apple_product_name:" + product)).Result()
	if err != nil {
		if err != redis.Nil {
			log.Error("get apple_product_name from redis failed", zap.String("product", product), zap.Any("name", name), zap.Error(err))
		}
		return product
	}
	return name
}

// SetStoreName set apple store name
func SetStoreName(store, name string) bool {
	err := client.Set(wrapKey("apple_store_name:"+store), name, 24*time.Hour).Err()
	if err != nil {
		if err != redis.Nil {
			log.Error("set apple_store_name to redis failed", zap.String("store", store), zap.Any("name", name), zap.Error(err))
		}
		return false
	}
	return true
}

// GetStoreName get apple store name
func GetStoreName(store string) string {
	name, err := client.Get(wrapKey("apple_store_name:" + store)).Result()
	if err != nil {
		log.Error("get apple_store_name from redis failed", zap.String("store", store), zap.Any("name", name), zap.Error(err))
		return store
	}
	return name
}
