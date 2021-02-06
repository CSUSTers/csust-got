package main

import (
	"csust-got/base"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/prom"
	"csust-got/restrict"
	"csust-got/util"
	"time"

	. "gopkg.in/tucnak/telebot.v2"

	"go.uber.org/zap"
)

func main() {
	config.InitConfig("config.yaml", "BOT")
	log.InitLogger()
	defer log.Sync()
	prom.InitPrometheus()
	orm.InitRedis()

	orm.LoadWhiteList()
	orm.LoadBlackList()

	bot, err := initBot()
	if err != nil {
		log.Fatal(err.Error())
		return
	}

	bot.Handle("/hello", base.Hello)
	bot.Handle("/say_hello", base.Hello)
	bot.Handle("/hello_to_all", base.HelloToAll)

	bot.Handle("/id", util.PrivateCommand(base.GetUserID))
	bot.Handle("/cid", base.GetChatID)
	bot.Handle("/info", base.Info)
	bot.Handle("/links", base.Links)

	// bot.Handle("/history", base.History)
	bot.Handle("/forward", base.Forward)
	bot.Handle("/mc", util.GroupCommand(base.MC))

	bot.Handle("/sleep", base.Sleep)
	bot.Handle("/no_sleep", base.NoSleep)

	bot.Handle("/google", base.Google)
	bot.Handle("/bing", base.Bing)
	bot.Handle("/bilibili", base.Bilibili)
	bot.Handle("/github", base.Github)

	bot.Handle("/recorder", base.Repeat)

	bot.Handle("/hitokoto", base.Hitokoto)
	bot.Handle("/hitowuta", base.HitDawu)
	bot.Handle("/hitdawu", base.HitDawu)
	bot.Handle("/hito_netease", base.HitoNetease)

	bot.Handle("/run_after", base.RunTask)

	bot.Handle("/fake_ban_myself", base.FakeBanMyself)
	bot.Handle("/fake_ban", util.GroupCommand(restrict.FakeBan))
	bot.Handle("/kill", util.GroupCommand(restrict.Kill))
	bot.Handle("/ban_myself", util.GroupCommand(restrict.BanMyself))
	bot.Handle("/ban", util.GroupCommand(restrict.Ban))
	bot.Handle("/ban_soft", util.GroupCommand(restrict.SoftBan))
	bot.Handle("/no_sticker", util.GroupCommand(restrict.NoSticker))
	bot.Handle("/shutdown", util.GroupCommand(base.Shutdown))
	bot.Handle("/halt", util.GroupCommand(base.Shutdown))
	bot.Handle("/boot", util.GroupCommand(base.Boot))

	bot.Handle("/yiban", util.PrivateCommand(base.Yiban))
	bot.Handle("/sub_yiban", util.PrivateCommand(base.SubYiban))
	bot.Handle("/no_yiban", util.PrivateCommand(base.NoYiban))

	bot.Handle(OnUserJoined, base.WelcomeNewMember)
	// bot.Handle(OnUserLeft, base.LeftMember)

	go base.YibanService()

	bot.Start()
}

func initBot() (*Bot, error) {
	panicReporter := func(err error) {
		log.Error("bot recover form panic", zap.Error(err))
	}

	settings := Settings{
		Token:     config.BotConfig.Token,
		Updates:   512,
		ParseMode: ModeDefault,
		Reporter:  panicReporter,
		Poller:    initPoller(),
	}
	if config.BotConfig.DebugMode {
		settings.Verbose = true
	}

	bot, err := NewBot(settings)
	if err != nil {
		return nil, err
	}

	config.BotConfig.Bot = bot
	log.Info("Success Authorized", zap.String("botUserName", bot.Me.Username))
	return bot, nil
}

func initPoller() *MiddlewarePoller {
	defaultPoller := &LongPoller{Timeout: 10 * time.Second}
	blackListPoller := NewMiddlewarePoller(defaultPoller, blackListFilter)
	fakeBanPoller := NewMiddlewarePoller(blackListPoller, fakeBanFilter)
	fakeBanPoller.Capacity = 16
	rateLimitPoller := NewMiddlewarePoller(fakeBanPoller, rateLimitFilter)
	noStickerPoller := NewMiddlewarePoller(rateLimitPoller, noStickerFilter)
	noStickerPoller.Capacity = 16
	promPoller := NewMiddlewarePoller(noStickerPoller, promFilter)
	shutdownPoller := NewMiddlewarePoller(promPoller, shutdownFilter)
	shutdownPoller.Capacity = 16
	return shutdownPoller
}

func blackListFilter(update *Update) bool {
	if update.Message == nil {
		return true
	}
	m := update.Message
	if config.BotConfig.BlackListConfig.Check(m.Chat.ID) {
		log.Info("message ignore by black list", zap.String("chat", m.Chat.Title))
		return false
	}
	if config.BotConfig.BlackListConfig.Check(int64(m.Sender.ID)) {
		log.Info("message ignore by black list", zap.String("user", m.Sender.Username))
		return false
	}
	return true
}

func fakeBanFilter(update *Update) bool {
	if update.Message == nil {
		return true
	}
	m := update.Message
	if orm.IsBanned(m.Chat.ID, m.Sender.ID) {
		util.DeleteMessage(m)
		log.Info("message deleted by fake ban", zap.String("chat", m.Chat.Title),
			zap.String("user", m.Sender.Username))
		return false
	}
	return true
}

func rateLimitFilter(update *Update) bool {
	if update.Message == nil || update.Message.Private() {
		return true
	}
	m := update.Message
	whiteListConfig := config.BotConfig.WhiteListConfig
	if !whiteListConfig.Enabled || !whiteListConfig.Check(m.Chat.ID) {
		return true
	}
	if !restrict.CheckLimit(m) {
		log.Info("message deleted by rate limit", zap.String("chat", m.Chat.Title),
			zap.String("user", m.Sender.Username))
		return false
	}
	return true
}

func shutdownFilter(update *Update) bool {
	if update.Message == nil {
		return true
	}
	m := update.Message
	if m.Text != "" {
		cmd := entities.FromMessage(m)
		if cmd != nil && cmd.Name() == "boot" {
			return true
		}
	}
	if orm.IsShutdown(m.Chat.ID) {
		log.Info("message ignore by shutdown", zap.String("chat", m.Chat.Title),
			zap.String("user", m.Sender.Username))
		return false
	}
	return true
}

func noStickerFilter(update *Update) bool {
	if update.Message == nil || update.Message.Sticker == nil {
		return true
	}
	m := update.Message
	if !orm.IsShutdown(m.Chat.ID) && orm.IsNoStickerMode(m.Chat.ID) {
		util.DeleteMessage(m)
		log.Info("message deleted by no sticker", zap.String("chat", m.Chat.Title),
			zap.String("user", m.Sender.Username))
		return false
	}
	return true
}

func promFilter(update *Update) bool {
	prom.DailUpdate(update)
	if update.Message == nil {
		return true
	}
	m := update.Message
	command := entities.FromMessage(m)
	if command != nil {
		log.Info("bot receive command", zap.String("chat", m.Chat.Title),
			zap.String("user", m.Sender.Username), zap.String("command", m.Text))
	}
	return true
}
