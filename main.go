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
	"net/http"
	"net/url"
	"time"

	. "gopkg.in/tucnak/telebot.v3"

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

	bot.Handle("/hello", util.WrapHandler(base.Hello))
	bot.Handle("/say_hello", util.WrapHandler(base.Hello))
	bot.Handle("/hello_to_all", util.WrapHandler(base.HelloToAll))

	bot.Handle("/id", util.WrapHandler(util.PrivateCommand(base.GetUserID)))
	bot.Handle("/cid", util.WrapHandler(base.GetChatID))
	bot.Handle("/info", util.WrapHandler(base.Info))
	bot.Handle("/links", util.WrapHandler(base.Links))

	// bot.Handle("/history", base.History)
	bot.Handle("/forward", util.WrapHandler(util.GroupCommand(base.Forward)))
	bot.Handle("/mc", util.WrapHandler(util.GroupCommand(base.MC)))

	bot.Handle("/sleep", util.WrapHandler(base.Sleep))
	bot.Handle("/no_sleep", util.WrapHandler(base.NoSleep))

	bot.Handle("/google", util.WrapHandler(base.Google))
	bot.Handle("/bing", util.WrapHandler(base.Bing))
	bot.Handle("/bilibili", util.WrapHandler(base.Bilibili))
	bot.Handle("/github", util.WrapHandler(base.Github))

	bot.Handle("/recorder", util.WrapHandler(base.Repeat))

	bot.Handle("/hitokoto", util.WrapHandler(base.Hitokoto))
	bot.Handle("/hitowuta", util.WrapHandler(base.HitDawu))
	bot.Handle("/hitdawu", util.WrapHandler(base.HitDawu))
	bot.Handle("/hito_netease", util.WrapHandler(base.HitoNetease))

	bot.Handle("/hugencoder", util.WrapHandler(base.HugeEncoder))
	bot.Handle("/hugedecoder", util.WrapHandler(base.HugeDecoder))

	bot.Handle("/run_after", util.WrapHandler(base.RunTask))

	bot.Handle("/fake_ban_myself", util.WrapHandler(base.FakeBanMyself))
	bot.Handle("/fake_ban", util.WrapHandler(util.GroupCommand(restrict.FakeBan)))
	bot.Handle("/kill", util.WrapHandler(util.GroupCommand(restrict.Kill)))
	bot.Handle("/ban_myself", util.WrapHandler(util.GroupCommand(restrict.BanMyself)))
	bot.Handle("/ban", util.WrapHandler(util.GroupCommand(restrict.Ban)))
	bot.Handle("/ban_soft", util.WrapHandler(util.GroupCommand(restrict.SoftBan)))
	bot.Handle("/no_sticker", util.WrapHandler(util.GroupCommand(restrict.NoSticker)))
	bot.Handle("/shutdown", util.WrapHandler(util.GroupCommand(base.Shutdown)))
	bot.Handle("/halt", util.WrapHandler(util.GroupCommand(base.Shutdown)))
	bot.Handle("/boot", util.WrapHandler(util.GroupCommand(base.Boot)))

	bot.Handle(OnUserJoined, util.WrapHandler(base.WelcomeNewMember))
	// bot.Handle(OnUserLeft, base.LeftMember)

	bot.Start()
}

func initBot() (*Bot, error) {
	panicReporter := func(err error) {
		log.Error("bot recover form panic", zap.Error(err))
	}

	httpClient := http.DefaultClient

	if config.BotConfig.Proxy != "" {
		proxyURL, err := url.Parse(config.BotConfig.Proxy)
		if err != nil {
			log.Fatal("proxy is wrong!", zap.Error(err))
		}
		httpClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	}

	settings := Settings{
		Token:     config.BotConfig.Token,
		Updates:   512,
		ParseMode: ModeDefault,
		OnError: func(e error, c Context) {
			panicReporter(e)
		},
		Poller: initPoller(),
		Client: httpClient,
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
