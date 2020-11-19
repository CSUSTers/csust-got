package main

import (
	"csust-got/base"
	"csust-got/config"
	"csust-got/context"
	"csust-got/manage"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
	"csust-got/prom"
	"csust-got/timer"
	"sync"
	"time"

	"github.com/go-redis/redis/v7"
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
	if list, err := rc.SMembers(ctx.WrapKey("black_black_list")).Result(); err != nil && err != redis.Nil {
		// dont do anything, maybe. (΄◞ิ౪◟ิ‵)
	} else {
		zap.L().Sugar().Infof("Black List has %d people.\n", len(list))
	}

	handles := module.Parallel([]module.Module{
		module.Stateless(base.Hello, preds.IsCommand("say_hello")),
		module.Stateless(base.GetUserID, preds.IsCommand("id")),
		module.Stateless(base.GetChatID, preds.IsCommand("cid")),
		module.Stateless(base.WelcomeNewMember, preds.NonEmpty),
		module.Stateless(base.HelloToAll, preds.IsCommand("hello_to_all")),
		module.Stateless(manage.BanMyself, preds.IsCommand("ban_myself")),
		module.Stateless(base.FakeBanMyself, preds.IsCommand("fake_ban_myself")),
		module.Stateless(manage.Ban, preds.IsCommand("ban")),
		module.Stateless(manage.SoftBan, preds.IsCommand("ban_soft")),
		module.WithPredicate(base.Hitokoto, preds.IsCommand("hitokoto")),
		module.WithPredicate(base.HitDawu, preds.IsCommand("hitowuta").Or(preds.IsCommand("hitdawu"))),
		module.WithPredicate(base.HitoNetease, preds.IsCommand("hito_netease")),
		base.Google,
		base.Bing,
		base.Bilibili,
		base.Github,
		base.Repeat,
		timer.RunTask(),
	})
	messageCounterModule := module.IsolatedChat(func(update tgbotapi.Update) module.Module {
		return module.SharedContext([]module.Module{
			module.WithPredicate(base.MC(), preds.IsCommand("mc")),
			base.MessageCount(),
		})
	})
	noStickerModule := module.SharedContext([]module.Module{
		module.WithPredicate(module.IsolatedChat(manage.NoSticker), preds.IsCommand("no_sticker")),
		module.WithPredicate(module.IsolatedChat(manage.DeleteSticker), preds.HasSticker)})
	handles = module.Sequential([]module.Module{
		module.NamedModule(module.IsolatedChat(manage.FakeBan), "fake_ban"),
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
				handles.HandleUpdate(ctx, update, bot)
				cost := time.Since(start)
				prom.DailUpdate(update, cost)
			}
		}()
	}
	wg.Wait()
}
