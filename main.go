package main

import (
	"csust-got/base"
	"csust-got/config"
	"csust-got/context"
	"csust-got/manage"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
	"csust-got/timer"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
	"net/http"
	_ "net/http/pprof"
)

func main() {
	bot, err := tgbotapi.NewBotAPI(config.BotConfig.Token)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = config.BotConfig.DebugMode

	log.Printf("Authorized on account %s", bot.Self.UserName)

	if bot.Debug {
		go func() {
			err := http.ListenAndServe(":8080", nil)
			if err != nil {
				log.Println(err.Error())
			}
		}()
	}

	config.BotConfig.Bot = bot

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)

	ctx := context.Global(orm.GetClient(), config.BotConfig)
	handles := module.Parallel([]module.Module{
		module.Stateless(base.Hello, preds.IsCommand("say_hello")),
		module.Stateless(base.WelcomeNewMember, preds.NonEmpty),
		module.Stateless(base.HelloToAll, preds.IsCommand("hello_to_all")),
		module.Stateless(manage.BanMyself, preds.IsCommand("ban_myself")),
		module.Stateless(base.FakeBanMyself, preds.IsCommand("fake_ban_myself")),
		module.Stateless(manage.Ban, preds.IsCommand("ban")),
		module.Stateless(manage.SoftBan, preds.IsCommand("ban_soft")),
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
	noStcikerModule := module.SharedContext([]module.Module{
		module.WithPredicate(module.IsolatedChat(manage.NoSticker), preds.IsCommand("no_sticker")),
		module.WithPredicate(module.IsolatedChat(manage.DeleteSticker), preds.HasSticker)})
	handles = module.Sequential([]module.Module{
		module.NewNamedModule(module.IsolatedChat(manage.FakeBan), "fake_ban"),
		module.NewDeferredModule(module.NewNamedModule(module.IsolatedChat(base.Shutdown), "shutdown")),
		module.NewNamedModule(noStcikerModule, "no_sticker"),
		module.NewNamedModule(messageCounterModule, "long_wang"),
		module.NewNamedModule(handles, "generic_modules"),
	})

	for update := range updates {
		go handles.HandleUpdate(ctx, update, bot)
	}
}
