package main

import (
	"csust-got/sd"
	"net/http"
	"net/url"
	"time"

	"csust-got/base"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/iwatch"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/prom"
	"csust-got/restrict"
	"csust-got/util"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

func main() {
	config.InitConfig("config.yaml", "BOT")
	log.InitLogger()
	defer log.Sync()
	prom.InitPrometheus()
	orm.InitRedis()

	orm.LoadWhiteList()
	orm.LoadBlockList()

	go iwatch.WatchService()

	bot, err := initBot()
	if err != nil {
		log.Panic(err.Error())
	}

	registerBaseHandler(bot)
	registerRestrictHandler(bot)
	registerEventHandler(bot)
	bot.Handle("/iwatch", util.PrivateCommand(iwatch.WatchHandler))
	bot.Handle("/sd", sd.Handler)
	bot.Handle("/sdcfg", sd.ConfigHandler)

	go sd.Process()

	base.Init()

	bot.Start()
}

func initBot() (*Bot, error) {
	errorHandler := func(err error, c Context) {
		log.Error("bot has error", zap.Any("context", c), zap.Error(err))
	}

	httpClient := http.DefaultClient

	if config.BotConfig.Proxy != "" {
		proxyURL, err := url.Parse(config.BotConfig.Proxy)
		if err != nil {
			log.Panic("proxy is wrong!", zap.Error(err))
		}
		httpClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	}

	settings := Settings{
		Token:     config.BotConfig.Token,
		Updates:   512,
		ParseMode: ModeDefault,
		OnError:   errorHandler,
		Poller:    &LongPoller{Timeout: 10 * time.Second},
		Client:    httpClient,
		Verbose:   false,
	}

	bot, err := NewBot(settings)
	if err != nil {
		return nil, err
	}

	bot.Use(loggerMiddleware, skipMiddleware, blockMiddleware, fakeBanMiddleware,
		rateMiddleware, noStickerMiddleware, promMiddleware, shutdownMiddleware)

	config.BotConfig.Bot = bot
	log.Info("Success Authorized", zap.String("botUserName", bot.Me.Username))
	return bot, nil
}

func registerBaseHandler(bot *Bot) {
	bot.Handle("/hello", base.Hello)
	bot.Handle("/say_hello", base.Hello)
	bot.Handle("/hello_to_all", base.HelloToAll)

	bot.Handle("/id", util.PrivateCommand(base.GetUserID))
	bot.Handle("/cid", base.GetChatID)
	bot.Handle("/info", base.Info)
	bot.Handle("/links", base.Links)

	// bot.Handle("/history", base.History)
	bot.Handle("/forward", util.GroupCommand(base.Forward))
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

	bot.Handle("/hugencoder", base.HugeEncoder)
	bot.Handle("/hugedecoder", base.HugeDecoder)

	bot.Handle("/run_after", base.RunTask)

	bot.Handle("/getvoice_old", base.GetVoice)
	bot.Handle("/getvoice", base.GetVoiceV2)
}

func registerRestrictHandler(bot *Bot) {
	bot.Handle("/fake_ban_myself", base.FakeBanMyself)
	bot.Handle("/fake_ban", util.GroupCommand(restrict.FakeBan))
	bot.Handle("/kill", util.GroupCommand(restrict.Kill))
	bot.Handle("/ban_myself", util.GroupCommand(restrict.BanMyself))
	bot.Handle("/ban", util.GroupCommand(restrict.BanCommand))
	bot.Handle("/ban_soft", util.GroupCommand(restrict.SoftBanCommand))
	bot.Handle("/no_sticker", util.GroupCommand(restrict.NoSticker))
	bot.Handle("/shutdown", util.GroupCommand(base.Shutdown))
	bot.Handle("/halt", util.GroupCommand(base.Shutdown))
	bot.Handle("/boot", util.GroupCommand(base.Boot))
}

func registerEventHandler(bot *Bot) {
	bot.Handle(OnUserJoined, base.WelcomeNewMember)
	// bot.Handle(OnUserLeft, base.LeftMember)
	bot.Handle(OnText, base.DoNothing)
	bot.Handle(OnSticker, base.DoNothing)
	bot.Handle(OnAnimation, base.DoNothing)
	bot.Handle(OnMedia, base.DoNothing)
	bot.Handle(OnPhoto, base.DoNothing)
	bot.Handle(OnVideo, base.DoNothing)
	bot.Handle(OnVoice, base.DoNothing)
	bot.Handle(OnVideoNote, base.DoNothing)
	bot.Handle(OnDocument, base.DoNothing)
}

func loggerMiddleware(next HandlerFunc) HandlerFunc {
	return func(ctx Context) error {
		log.Debug("bot receive update", zap.Any("update", ctx.Update()))
		return next(ctx)
	}
}

func skipMiddleware(next HandlerFunc) HandlerFunc {
	skipSec := config.BotConfig.SkipDuration
	return func(ctx Context) error {
		m := ctx.Message()
		if m == nil {
			return next(ctx)
		}
		d := time.Since(m.Time())
		if skipSec > 0 && int64(d.Seconds()) > skipSec {
			log.Debug("bot skip expired update", zap.Any("update", ctx.Update()))
			return nil
		}
		return next(ctx)
	}
}

func blockMiddleware(next HandlerFunc) HandlerFunc {
	return func(ctx Context) error {
		if ctx.Chat() != nil && config.BotConfig.BlockListConfig.Check(ctx.Chat().ID) {
			log.Info("chat ignore by block list", zap.String("chat", ctx.Chat().Title))
			return nil
		}
		if ctx.Sender() != nil && config.BotConfig.BlockListConfig.Check(ctx.Sender().ID) {
			log.Info("sender ignore by block list", zap.String("user", ctx.Sender().Username))
			return nil
		}
		return next(ctx)
	}
}

func fakeBanMiddleware(next HandlerFunc) HandlerFunc {
	return func(ctx Context) error {
		if !isChatMessageHasSender(ctx) {
			return next(ctx)
		}
		if orm.IsBanned(ctx.Chat().ID, ctx.Sender().ID) {
			util.DeleteMessage(ctx.Message())
			log.Info("message deleted by fake ban", zap.String("chat", ctx.Chat().Title),
				zap.String("user", ctx.Sender().Username))
			return nil
		}
		return next(ctx)
	}
}

func rateMiddleware(next HandlerFunc) HandlerFunc {
	whiteListConfig := config.BotConfig.WhiteListConfig
	return func(ctx Context) error {
		if !isChatMessageHasSender(ctx) || ctx.Chat().Type == ChatPrivate {
			return next(ctx)
		}
		if !whiteListConfig.Enabled || !whiteListConfig.Check(ctx.Chat().ID) {
			return next(ctx)
		}
		if !restrict.CheckLimit(ctx.Message()) {
			log.Info("message deleted by rate limit", zap.String("chat", ctx.Chat().Title),
				zap.String("user", ctx.Sender().Username))
			return nil
		}
		return next(ctx)
	}
}

func noStickerMiddleware(next HandlerFunc) HandlerFunc {
	return func(ctx Context) error {
		if !isChatMessageHasSender(ctx) || ctx.Message().Sticker == nil {
			return next(ctx)
		}
		if !orm.IsShutdown(ctx.Chat().ID) && orm.IsNoStickerMode(ctx.Chat().ID) {
			util.DeleteMessage(ctx.Message())
			log.Info("message deleted by no sticker", zap.String("chat", ctx.Chat().Title),
				zap.String("user", ctx.Sender().Username))
			return nil
		}
		return next(ctx)
	}
}

func promMiddleware(next HandlerFunc) HandlerFunc {
	return func(ctx Context) error {
		if ctx.Message() == nil {
			return next(ctx)
		}
		prom.DialContext(ctx)
		command := entities.FromMessage(ctx.Message())
		if command != nil {
			log.Info("bot receive command", zap.String("chat", ctx.Chat().Title),
				zap.String("user", ctx.Sender().Username), zap.String("command", ctx.Message().Text))
		}
		return next(ctx)
	}
}

func shutdownMiddleware(next HandlerFunc) HandlerFunc {
	return func(ctx Context) error {
		if !isChatMessageHasSender(ctx) {
			return next(ctx)
		}
		if ctx.Message().Text != "" {
			cmd := entities.FromMessage(ctx.Message())
			if cmd != nil && cmd.Name() == "boot" {
				return next(ctx)
			}
		}
		if orm.IsShutdown(ctx.Chat().ID) {
			log.Info("message ignore by shutdown", zap.String("chat", ctx.Chat().Title),
				zap.String("user", ctx.Sender().Username))
			return nil
		}
		return next(ctx)
	}
}

func isChatMessageHasSender(ctx Context) bool {
	return ctx.Chat() != nil && ctx.Message() != nil && ctx.Sender() != nil
}
