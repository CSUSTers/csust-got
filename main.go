package main

import (
	"csust-got/base"
	"csust-got/config"
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
	"csust-got/prom"
	"csust-got/restrict"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

var worker = 4

func main() {
	bot, err := tgbotapi.NewBotAPI(config.BotConfig.Token)
	if err != nil {
		zap.L().Panic(err.Error())
	}

	bot.Debug = config.BotConfig.DebugMode

	zap.L().Sugar().Infof("Authorized on account %s", bot.Self.UserName)

	config.BotConfig.Bot = bot

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)

	ctx := context.Global(orm.GetClient(), config.BotConfig)

	// check database
	rc := ctx.GlobalClient()
	// blacklsit
	if list, err := rc.SMembers(ctx.WrapKey("black_black_list")).Result(); err != nil {
		// dont do anything, maybe. (΄◞ิ౪◟ิ‵)
	} else {
		zap.L().Sugar().Infof("Black List has %d people.\n", len(list))
	}

	handles := module.Parallel([]module.Module{
		module.Stateless(base.Hello, preds.IsCommand("say_hello")),
		module.Stateless(base.GetUserID, preds.IsCommand("id")),
		module.Stateless(base.GetChatID, preds.IsCommand("cid")),
		module.Stateless(base.History, preds.IsCommand("history")),
		module.Stateless(base.Forward, preds.IsCommand("forward")),
		module.Stateless(base.Info, preds.IsCommand("info")),
		module.Stateless(base.Links, preds.IsCommand("links")),
		module.Stateless(base.Sleep, preds.IsCommand("sleep")),
		module.Stateless(base.NoSleep, preds.IsCommand("no_sleep")),
		module.Stateless(base.WelcomeNewMember, preds.NonEmpty),
		module.Stateless(base.HelloToAll, preds.IsCommand("hello_to_all")),
		module.Stateless(restrict.BanMyself, preds.IsCommand("ban_myself")),
		module.Stateless(base.FakeBanMyself, preds.IsCommand("fake_ban_myself")),
		module.Stateless(restrict.Ban, preds.IsCommand("ban")),
		module.Stateless(restrict.SoftBan, preds.IsCommand("ban_soft")),
		module.WithPredicate(base.Hitokoto, preds.IsCommand("hitokoto")),
		module.WithPredicate(base.HitDawu, preds.IsCommand("hitowuta").Or(preds.IsCommand("hitdawu"))),
		module.WithPredicate(base.HitoNetease, preds.IsCommand("hito_netease")),
		base.Google,
		base.Bing,
		base.Bilibili,
		base.Github,
		base.Repeat,
		base.RunTask(),
	})
	messageCounterModule := module.IsolatedChat(func(update tgbotapi.Update) module.Module {
		return module.SharedContext([]module.Module{
			module.WithPredicate(base.MC(), preds.IsCommand("mc")),
			base.MessageCount(),
		})
	})
	noStickerModule := module.SharedContext([]module.Module{
		module.WithPredicate(module.IsolatedChat(restrict.NoSticker), preds.IsCommand("no_sticker")),
		module.WithPredicate(module.IsolatedChat(restrict.DeleteSticker), preds.HasSticker)})
	handles = module.Sequential([]module.Module{
		module.NamedModule(module.IsolatedChat(restrict.FakeBan), "fake_ban"),
		module.NamedModule(module.IsolatedChat(restrict.RateLimit), "rate_limit"),
		module.NamedModule(module.IsolatedChat(base.Shutdown), "shutdown"),
		module.NamedModule(noStickerModule, "no_sticker"),
		module.NamedModule(module.DeferredModule(messageCounterModule), "long_wang"),
		module.NamedModule(handles, "generic_modules"),
	})

	wg := sync.WaitGroup{}
	wg.Add(worker)
	for i := 0; i < worker; i++ {
		go func() {
			defer wg.Done()
			for update := range updates {
				start := time.Now()
				result := handles.HandleUpdate(ctx, update, bot)
				cost := time.Since(start)
				prom.DailUpdate(update, result == module.NextOfChain, cost)
			}
		}()
	}
	wg.Wait()
}
