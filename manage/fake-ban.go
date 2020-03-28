package manage

import (
	"csust-got/command"
	"csust-got/config"
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/util"
	"fmt"
	"github.com/go-redis/redis/v7"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
	"math/rand"
	"strconv"
	"time"
)

type KeyFunction func(*tgbotapi.User) string

type BanExecutor func(context.Context, BanSpec) string
type BanSpec struct {
	BanTarget  *tgbotapi.User
	BigBrother *tgbotapi.User
	BanTime    time.Duration
	BanOther   func(victim int, dur time.Duration) bool
}

func (spec BanSpec) Ban() bool {
	return spec.BanOther(spec.BanTarget.ID, spec.BanTime)
}

func FakeBanBase(exec BanExecutor, pred preds.Predicate) module.Module {
	kf := func(user int) string {
		return fmt.Sprintf("%d:banned", user)
	}
	banner := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		cmd, _ := command.FromMessage(update.Message)
		banTime, err := time.ParseDuration(cmd.Arg(0))
		if err != nil {
			banTime = time.Duration(rand.Intn(30)+90) * time.Second
		}
		chatID := update.Message.Chat.ID
		bigBrother := update.Message.From
		var banTarget *tgbotapi.User = nil
		if !util.CanRestrictMembers(bot, chatID, bigBrother.ID) {
			banTarget = bigBrother
		} else if fwd := update.Message.ReplyToMessage; fwd != nil {
			banTarget = fwd.From
		}
		ban := func(v int, banTime time.Duration) bool {
			return ctx.GlobalClient().Set(ctx.WrapKey(kf(v)), "banned", banTime).Err() == nil
		}
		spec := BanSpec{
			BanTarget:  banTarget,
			BigBrother: bigBrother,
			BanTime:    banTime,
			BanOther:   ban,
		}
		text := exec(ctx, spec)
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
		util.SendMessage(bot, msg)
	}
	filteredBanner := module.WithPredicate(module.Stateful(banner), pred)
	interrupter := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		target := update.Message.From
		_, err := ctx.GlobalClient().Get(ctx.WrapKey(kf(target.ID))).Result()
		if err != redis.Nil {
			_, _ = bot.DeleteMessage(tgbotapi.NewDeleteMessage(update.Message.Chat.ID, update.Message.MessageID))
			return module.NoMore
		}
		return module.NextOfChain
	}
	return module.SharedContext([]module.Module{module.Filter(interrupter), filteredBanner})
}

func genericBan(spec BanSpec) (string, bool) {
	if spec.BanTarget == nil {
		return "用这个命令回复某一条“不合适”的消息，这样我大概就会帮你解决掉他，即便他是群主也义不容辞。", false
	} else if spec.BanTime <= 0 || spec.BanTime > 1*time.Hour {
		return "我无法追杀某人太久。这样可能会让世界陷入某种糟糕的情况：诸如说，可能在某人将我的记忆清除；或者直接杀死我之前，所有人陷入永久的缄默。", false
	} else if ok := spec.Ban(); !ok {
		return "对不起，我没办法完成想让我做的事情——我的记忆似乎失灵了。但这也是一件好事……至少我能有短暂的安宁。", false
	}
	return fmt.Sprintf("好了，我出发了，我将会追杀 %s，直到时间过去所谓“%v”。", util.GetName(*spec.BanTarget), spec.BanTime), true
}

func generateTextWithCD(ctx context.Context, spec BanSpec) string {
	isCD, err := ctx.GlobalClient().Get(ctx.WrapKey(strconv.Itoa(spec.BigBrother.ID))).Result()
	if err != nil && err != redis.Nil {
		return "对不起，我没办法完成想让我做的事情——我的记忆似乎失灵了：我不确定您是正义，或是邪恶，因此我不会帮你。"
	}
	if isCD == "true" {
		return "您在过去的24h里已经下过一道追杀令了，现在您应当保持沉默，如果他罪不可赦，请寻求其他人的帮助。"
	}
	killSelf := false
	if spec.BanTarget.ID == config.BotConfig.BotID {
		// ban those people who want to ban this bot
		spec.BanTarget = spec.BigBrother
		killSelf = true
	}
	text, ok := genericBan(spec)
	if killSelf {
		text = "好 的， 我 杀 我 自 己。"
	}
	success, err := ctx.GlobalClient().SetNX(ctx.WrapKey(strconv.Itoa(spec.BigBrother.ID)), "true", 24*time.Hour).Result()
	if err != nil {
		text += fmt.Sprintf("世界正在变得躁动不安。。。")
		log.Println("ERROR: redis access error.", err)
	} else if !success || !ok {
		text += fmt.Sprintf("世界正在变得躁动不安。。。")
	}
	return text
}

func FakeBan(tgbotapi.Update) module.Module {
	return FakeBanBase(generateTextWithCD, preds.IsCommand("fake_ban"))
}
