package orm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"csust-got/config"
	"csust-got/log"
	"csust-got/util"

	"github.com/redis/go-redis/v9"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
)

var rc *redis.Client

// InitRedis init redis.
func InitRedis() {
	rc = NewClient()
}

// NewClient new redis client.
func NewClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     config.BotConfig.RedisConfig.RedisAddr,
		Password: config.BotConfig.RedisConfig.RedisPass,
	})
}

// Ping can ping a redis client.
// return true if ping success.
func Ping(c *redis.Client) bool {
	// TODO: replace ctx with real ctx
	if _, err := c.Ping(context.TODO()).Result(); err != nil {
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

func wrapKeyWithChatMsg(key string, chatID int64, msgID int) string {
	cid := strconv.FormatInt(chatID, 10)
	mid := strconv.Itoa(msgID)
	return wrapKey(key + ":c" + cid + ":u" + mid)
}

func loadSpecialList(key string) []string {
	list, err := rc.SMembers(context.TODO(), wrapKey(key)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("load special list failed", zap.String("key", key), zap.Error(err))
		}
		list = make([]string, 0)
	}
	return list
}

// LoadWhiteList load white list.
func LoadWhiteList() {
	chats := util.StringsToInts(loadSpecialList("white_list"))
	log.Info("White List has load.", zap.Int("length", len(chats)))
	config.BotConfig.WhiteListConfig.Chats = chats
}

// LoadBlockList load black list.
func LoadBlockList() {
	chats := util.StringsToInts(loadSpecialList("black_list"))
	log.Info("Block List has load.", zap.Int("length", len(chats)))
	config.BotConfig.BlockListConfig.Chats = chats
}

// IsNoStickerMode check group in NoSticker mode.
func IsNoStickerMode(chatID int64) bool {
	ok, err := GetBool(wrapKeyWithChat("no_sticker", chatID))
	if err != nil {
		log.Error("get NoStickerMode failed", zap.Int64("chatID", chatID), zap.Error(err))
		return false
	}
	return ok
}

// ToggleNoStickerMode toggle NoSticker mode.
func ToggleNoStickerMode(chatID int64) bool {
	err := ToggleBool(wrapKeyWithChat("no_sticker", chatID))
	if err != nil {
		log.Error("get NoStickerMode failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
	return err == nil
}

// Shutdown bot.
func Shutdown(chatID int64) {
	err := WriteBool(wrapKeyWithChat("shutdown", chatID), true, 0)
	if err != nil {
		log.Error("Shutdown failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
}

// Boot boot bot.
func Boot(chatID int64) {
	err := WriteBool(wrapKeyWithChat("shutdown", chatID), false, 0)
	if err != nil {
		log.Error("boot failed", zap.Int64("chatID", chatID), zap.Error(err))
	}
}

// IsShutdown check bot is shutdown.
func IsShutdown(chatID int64) bool {
	ok, err := GetBool(wrapKeyWithChat("shutdown", chatID))
	if err != nil {
		log.Error("get Shutdown failed", zap.Int64("chatID", chatID), zap.Error(err))
		return false
	}
	return ok
}

// IsFakeBanInCD check fake ban is in cd.
func IsFakeBanInCD(chatID int64, userID int64) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("get IsFakeBanInCD failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
		return true
	}
	return ok
}

// IsBanned check someone is banned.
func IsBanned(chatID int64, userID int64) bool {
	ok, err := GetBool(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("get IsBanned failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
		return false
	}
	return ok
}

// GetBannedDuration get someone banned duration.
func GetBannedDuration(chatID int64, userID int64) time.Duration {
	sec, err := GetTTL(wrapKeyWithChatMember("banned", chatID, userID))
	if err != nil {
		log.Error("GetBannedDuration failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
	}
	return sec
}

// GetBannerDuration get fake ban cd.
func GetBannerDuration(chatID int64, userID int64) time.Duration {
	sec, err := GetTTL(wrapKeyWithChatMember("banner", chatID, userID))
	if err != nil {
		log.Error("GetBannerDuration failed", zap.Int64("chatID", chatID), zap.Int64("userID", userID), zap.Error(err))
	}
	return sec
}

// ResetBannedDuration reset banned duration.
func ResetBannedDuration(chatID int64, bannedID int64, d time.Duration) bool {
	// TODO: replace ctx with real ctx
	ok, err := rc.Expire(context.TODO(), wrapKeyWithChatMember("banned", chatID, bannedID), d).Result()
	if err != nil {
		log.Error("ResetBannedDuration failed", zap.Int64("chatID", chatID), zap.Int64("userID", bannedID), zap.Error(err))
		return false
	}
	return ok
}

// AddBanDuration add ban duration.
func AddBanDuration(chatID int64, bannerID, bannedID int64, ad time.Duration) bool {
	MakeBannerCD(chatID, bannerID, util.GetBanCD(ad))
	d := GetBannedDuration(chatID, bannedID)
	return d != 0 && ResetBannedDuration(chatID, bannedID, ad+d)
}

// Ban ban someone.
func Ban(chatID int64, bannerID, bannedID int64, d time.Duration) bool {
	MakeBannerCD(chatID, bannerID, util.GetBanCD(d))
	err := WriteBool(wrapKeyWithChatMember("banned", chatID, bannedID), true, d)
	if err != nil {
		log.Error("Ban failed", zap.Int64("chatID", chatID), zap.Int64("userID", bannedID), zap.Error(err))
		return false
	}
	return true
}

// MakeBannerCD make banner in cd.
func MakeBannerCD(chatID int64, bannerID int64, d time.Duration) bool {
	err := WriteBool(wrapKeyWithChatMember("banner", chatID, bannerID), true, d)
	if err != nil {
		log.Error("Ban set CD failed", zap.Int64("chatID", chatID), zap.Int64("userID", bannerID), zap.Error(err))
		return false
	}
	return true
}

// StoreHitokoto store hitokoto.
func StoreHitokoto(hitokoto string) {
	err := rc.SAdd(context.TODO(), wrapKey("hitokoto"), hitokoto).Err()
	if err != nil {
		log.Error("save hitokoto to redis failed", zap.Error(err))
	}
}

// GetHitokoto get hitokoto.
func GetHitokoto(from bool) string {
	res, err := rc.SRandMember(context.TODO(), wrapKey("hitokoto")).Result()
	if err != nil {
		log.Error("get hitokoto from redis failed", zap.Error(err))
		return config.BotConfig.MessageConfig.HitokotoNotFound
	}
	if !from {
		res = res[:strings.LastIndex(res, " by ")+1]
	}
	return res
}

// WatchStore watch Apple Store.
func WatchStore(userID int64, stores []string) bool {
	if len(stores) == 0 {
		return true
	}
	// add user to watcher
	if !AppleWatcherRegister(userID) {
		return false
	}

	// add store to user's watching list
	userStore := make([]interface{}, len(stores))
	for i, v := range stores {
		userStore[i] = v
	}
	err := rc.SAdd(context.TODO(), wrapKeyWithUser("watch_store", userID), userStore...).Err()
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

// RemoveStore not watch Apple Store.
func RemoveStore(userID int64, stores []string) bool {
	if len(stores) == 0 {
		return true
	}
	// remove store from user's watching list
	userStore := make([]interface{}, len(stores))
	for i, v := range stores {
		userStore[i] = v
	}
	err := rc.SRem(context.TODO(), wrapKeyWithUser("watch_store", userID), userStore...).Err()
	if err != nil {
		log.Error("remove store from redis failed", zap.Int64("user", userID), zap.Any("store", stores), zap.Error(err))
		return false
	}

	return true
}

// WatchProduct watch apple product.
func WatchProduct(userID int64, products []string) bool {
	if len(products) == 0 {
		return true
	}
	// add user to watcher
	if !AppleWatcherRegister(userID) {
		return false
	}

	// add products to user's watching list
	userProduct := make([]interface{}, len(products))
	for i, v := range products {
		userProduct[i] = v
	}
	err := rc.SAdd(context.TODO(), wrapKeyWithUser("watch_product", userID), userProduct...).Err()
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

// RemoveProduct not watch apple product.
func RemoveProduct(userID int64, products []string) bool {
	if len(products) == 0 {
		return true
	}
	// remove products from user's watching list
	userProduct := make([]interface{}, len(products))
	for i, v := range products {
		userProduct[i] = v
	}
	err := rc.SRem(context.TODO(), wrapKeyWithUser("watch_product", userID), userProduct...).Err()
	if err != nil {
		log.Error("remove product from redis failed", zap.Int64("user", userID), zap.Any("product", products), zap.Error(err))
		return false
	}

	return true
}

// AppleWatcherRegister apple watcher register.
func AppleWatcherRegister(userID int64) bool {
	err := rc.SAdd(context.TODO(), wrapKey("apple_watcher"), userID).Err()
	if err != nil {
		log.Error("register user to redis failed", zap.Int64("user", userID), zap.Error(err))
		return false
	}
	return true
}

// GetAppleWatcher get all apple watcher.
func GetAppleWatcher() ([]int64, bool) {
	users, err := rc.SMembers(context.TODO(), wrapKey("apple_watcher")).Result()
	if err != nil {
		log.Error("get apple user from redis failed", zap.Error(err))
		return []int64{}, false
	}
	userIDs := make([]int64, 0, len(users))
	for _, v := range users {
		userID, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs, true
}

// AppleTargetRegister apple product and store register.
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
	err := rc.SAdd(context.TODO(), wrapKey("apple_target"), targets...).Err()
	if err != nil {
		log.Error("register target to redis failed", zap.Any("target", targets), zap.Error(err))
		return false
	}

	return true
}

// AppleTargetRemove remove apple targets.
func AppleTargetRemove(targets ...string) bool {
	if len(targets) == 0 {
		return true
	}
	tar := make([]interface{}, len(targets))
	for i, v := range targets {
		tar[i] = v
	}

	// save to redis
	err := rc.SRem(context.TODO(), wrapKey("apple_target"), tar...).Err()
	if err != nil {
		log.Error("remove target from redis failed", zap.Any("target", targets), zap.Error(err))
		return false
	}

	return true
}

// GetWatchingStores get watching Apple Store of user.
func GetWatchingStores(userID int64) ([]string, bool) {
	stores, err := rc.SMembers(context.TODO(), wrapKeyWithUser("watch_store", userID)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Error("get stores of user from redis failed", zap.Int64("user", userID), zap.Error(err))
		return stores, false
	}
	return stores, true
}

// GetWatchingProducts get watching apple products of user.
func GetWatchingProducts(userID int64) ([]string, bool) {
	products, err := rc.SMembers(context.TODO(), wrapKeyWithUser("watch_product", userID)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Error("get products of user from redis failed", zap.Int64("user", userID), zap.Error(err))
		return products, false
	}
	return products, true
}

// GetTargetList get watching Apple Store and product.
func GetTargetList() ([]string, bool) {
	targets, err := rc.SMembers(context.TODO(), wrapKey("apple_target")).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Error("get targets from redis failed", zap.Error(err))
		return targets, false
	}
	return targets, true
}

// GetTargetMap get and cal target map: target -> userID -> exist.
func GetTargetMap() (map[string]map[int64]struct{}, bool) {
	userIDs, ok := GetAppleWatcher()
	if !ok {
		return nil, false
	}

	targetMap := make(map[string]map[int64]struct{})
	for _, userID := range userIDs {
		products, productsOK := GetWatchingProducts(userID)
		stores, storesOK := GetWatchingStores(userID)
		if !productsOK || !storesOK {
			log.Warn("get watcher list failed", zap.Int64("user", userID))
			continue
		}

		setTargetMap(userID, stores, products, targetMap)
	}

	return targetMap, true
}

func setTargetMap(userID int64, stores, products []string, targetMap map[string]map[int64]struct{}) {
	for _, store := range stores {
		for _, product := range products {
			target := product + ":" + store
			if _, ok := targetMap[target]; !ok {
				targetMap[target] = make(map[int64]struct{})
			}
			targetMap[target][userID] = struct{}{}
		}
	}
}

// SetProductName set apple product name.
func SetProductName(product, name string) bool {
	err := rc.Set(context.TODO(), wrapKey("apple_product_name:"+product), name, 24*time.Hour).Err()
	if err != nil {
		log.Error("set apple_product_name to redis failed", zap.String("product", product), zap.Any("name", name), zap.Error(err))
		return false
	}
	return true
}

// GetProductName get apple product name.
func GetProductName(product string) string {
	name, err := rc.Get(context.TODO(), wrapKey("apple_product_name:"+product)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get apple_product_name from redis failed", zap.String("product", product), zap.Any("name", name), zap.Error(err))
		}
		return product
	}
	return name
}

// SetStoreName set Apple Store name.
func SetStoreName(store, name string) bool {
	err := rc.Set(context.TODO(), wrapKey("apple_store_name:"+store), name, 24*time.Hour).Err()
	if err != nil {
		log.Error("set apple_store_name to redis failed", zap.String("store", store), zap.Any("name", name), zap.Error(err))
		return false
	}
	return true
}

// GetStoreName get Apple Store name.
func GetStoreName(store string) string {
	name, err := rc.Get(context.TODO(), wrapKey("apple_store_name:"+store)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get apple_store_name from redis failed", zap.String("store", store), zap.Any("name", name), zap.Error(err))
		}
		return store
	}
	return name
}

// SetTargetState set apple target last state.
func SetTargetState(target string, avaliable bool) {
	err := rc.Set(context.TODO(), wrapKey("apple_target_state:"+target), avaliable, 24*time.Hour).Err()
	if err != nil {
		log.Error("set apple_target_state to redis failed", zap.String("target", target), zap.Any("available", avaliable), zap.Error(err))
		return
	}
}

// GetTargetState get apple target last state.
func GetTargetState(target string) bool {
	r, err := rc.Get(context.TODO(), wrapKey("apple_target_state:"+target)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get apple_target_state from redis failed", zap.String("target", target), zap.Error(err))
		}
		return false
	}
	return r == "1"
}

// SetSDConfig set stable diffusion config.
func SetSDConfig(userID int64, cfg string) error {
	err := rc.Set(context.TODO(), wrapKeyWithUser("stable_diffusion_config", userID), cfg, 0).Err()
	if err != nil {
		log.Error("set stable diffusion config to redis failed", zap.Int64("user", userID), zap.String("config", cfg), zap.Error(err))
		return err
	}
	return nil
}

// GetSDConfig get stable diffusion config.
func GetSDConfig(userID int64) (string, error) {
	cfg, err := rc.Get(context.TODO(), wrapKeyWithUser("stable_diffusion_config", userID)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get stable diffusion config from redis failed", zap.Int64("user", userID), zap.Error(err))
		}
		return "", err
	}
	return cfg, nil
}

// SetSDLastPrompt save user's last stable diffusion prompt.
func SetSDLastPrompt(userID int64, lastPrompt string) error {
	err := rc.Set(context.TODO(), wrapKeyWithUser("stable_diffusion_last_prompt", userID), lastPrompt, 0).Err()
	if err != nil {
		log.Error("set stable diffusion last prompt to redis failed", zap.Int64("user", userID), zap.String("lastPrompt", lastPrompt), zap.Error(err))
		return err
	}
	return nil
}

// GetSDLastPrompt get user's last stable diffusion prompt.
func GetSDLastPrompt(userID int64) (string, error) {
	lastPrompt, err := rc.Get(context.TODO(), wrapKeyWithUser("stable_diffusion_last_prompt", userID)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get stable diffusion last prompt from redis failed", zap.Int64("user", userID), zap.Error(err))
			return "", err
		}
		return "", err
	}
	return lastPrompt, nil
}

// GetSDDefaultServer get stable diffusion default server from redis.
func GetSDDefaultServer() string {
	defaultServer, err := rc.Get(context.TODO(), wrapKey("stable_diffusion::default_server")).Result()
	if err != nil {
		return ""
	}
	return defaultServer
}

// SetChatContext save user's chat context with GPT to redis.
func SetChatContext(chatID int64, msgID int, chatContext []openai.ChatCompletionMessage) error {
	if len(chatContext) == 0 {
		return nil
	}
	if chatContext[0].Role == "system" {
		chatContext = chatContext[1:]
	}
	chatContextJSON, err := json.Marshal(chatContext)
	if err != nil {
		log.Error("marshal chat context failed", zap.Int64("chat", chatID), zap.Int("msg", msgID), zap.Error(err))
		return err
	}
	err = rc.Set(context.TODO(), wrapKeyWithChatMsg("chat_context", chatID, msgID), chatContextJSON, 7*24*time.Hour).Err()
	if err != nil {
		log.Error("set chat context to redis failed", zap.Int64("chat", chatID), zap.Int("msg", msgID), zap.Error(err))
		return err
	}
	return nil
}

// GetChatContext get user's chat context with GPT from redis.
func GetChatContext(chatID int64, msgID int) ([]openai.ChatCompletionMessage, error) {
	chatContextJSON, err := rc.Get(context.TODO(), wrapKeyWithChatMsg("chat_context", chatID, msgID)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get chat context from redis failed", zap.Int64("chat", chatID), zap.Int("msg", msgID), zap.Error(err))
		}
		return nil, err
	}
	var chatContext []openai.ChatCompletionMessage
	err = json.Unmarshal([]byte(chatContextJSON), &chatContext)
	if err != nil {
		log.Error("unmarshal chat context failed", zap.Int64("chat", chatID), zap.Int("msg", msgID), zap.Error(err))
		return nil, err
	}
	return chatContext, nil
}

// LoadGachaSession load gacha settings of a certain session from redis.
func LoadGachaSession(chatID int64) (config.GachaTenant, error) {
	tenantJSON, err := rc.Get(context.TODO(), wrapKeyWithChat("gacha_tenant", chatID)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get gacha tenant from redis failed", zap.Int64("chat", chatID), zap.Error(err))
		}
		return config.GachaTenant{}, err
	}
	var tenant config.GachaTenant
	err = json.Unmarshal([]byte(tenantJSON), &tenant)
	if err != nil {
		log.Error("unmarshal gacha tenant failed", zap.Int64("chat", chatID), zap.Error(err))
		return config.GachaTenant{}, err
	}
	return tenant, nil
}

// SaveGachaSession save gacha settings of a certain session to redis.
func SaveGachaSession(chatID int64, tenant config.GachaTenant) error {
	tenantJSON, err := json.Marshal(tenant)
	if err != nil {
		log.Error("marshal gacha tenant failed", zap.Int64("chat", chatID), zap.Error(err))
		return err
	}
	err = rc.Set(context.TODO(), wrapKeyWithChat("gacha_tenant", chatID), tenantJSON, 42*24*time.Hour).Err()
	if err != nil {
		log.Error("set gacha tenant to redis failed", zap.Int64("chat", chatID), zap.Error(err))
		return err
	}
	return nil
}

// SetByeWorldDuration save bye world duration to redis.
func SetByeWorldDuration(chatID int64, userID int64, duration time.Duration) error {
	err := rc.Set(context.TODO(), wrapKeyWithChatMember("bye_world", chatID, userID), duration.String(), 7*24*time.Hour).Err()
	if err != nil {
		log.Error("set bye world duration to redis failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
		return err
	}
	return nil
}

// DeleteByeWorldDuration delete bye world duration from redis.
func DeleteByeWorldDuration(chatID int64, userID int64) error {
	err := rc.Del(context.TODO(), wrapKeyWithChatMember("bye_world", chatID, userID)).Err()
	if err != nil {
		log.Error("delete bye world duration from redis failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
		return err
	}
	return nil
}

// IsByeWorld check user is in bye world mode.
func IsByeWorld(chatID int64, userID int64) (time.Duration, bool, error) {
	d, err := rc.Get(context.TODO(), wrapKeyWithChatMember("bye_world", chatID, userID)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get bye world duration from redis failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
			return 0, false, err
		}
		return 0, false, nil
	}
	duration, err := time.ParseDuration(d)
	if err != nil {
		log.Error("parse bye world duration failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
		return 0, false, err
	}
	return duration, true, nil
}

// KeepByeWorldDuration keep bye world duration.
func KeepByeWorldDuration(chatID int64, userID int64) {
	_, err := rc.Expire(context.TODO(), wrapKeyWithChatMember("bye_world", chatID, userID), 7*24*time.Hour).Result()
	if err != nil {
		log.Error("keep bye world duration failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
		return
	}
}
