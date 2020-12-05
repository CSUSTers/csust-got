package restrict

import (
	"csust-got/entities"
	"csust-got/config"
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/util"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

// KeyFunction is function
type KeyFunction func(*tgbotapi.User) string

// BanExecutor is executor
type BanExecutor func(context.Context, BanSpec) string

//BanSpec is ban spec
type BanSpec struct {
	BanTarget  *tgbotapi.User
	BigBrother *tgbotapi.User
	BanTime    time.Duration
	BanOther   func(victim int, dur time.Duration) bool
}

// Ban is executor of fake ban.
func (spec BanSpec) Ban() bool {
	return spec.BanOther(spec.BanTarget.ID, spec.BanTime)
}

// FakeBanBase is base module of fake ban.
func FakeBanBase(exec BanExecutor, pred preds.Predicate) module.Module {
	kf := func(user int) string {
		return fmt.Sprintf("%d:banned", user)
	}
	banner := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		cmd, _ := entities.FromMessage(update.Message)
		banTime, err := time.ParseDuration(cmd.Arg(0))
		if err != nil {
			banTime = time.Duration(60+rand.Intn(60)) * time.Second
		}
		//chatID := update.Message.Chat.ID
		bigBrother := update.Message.From
		var banTarget *tgbotapi.User = nil
		if fwd := update.Message.ReplyToMessage; fwd != nil {
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
		if cmd.Name() == "kill" {
			spec.BanTime = 10 * time.Minute
		}
		text := exec(ctx, spec)
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
		util.SendMessage(bot, msg)
	}
	filteredBanner := module.WithPredicate(module.Stateful(banner), pred)
	interrupter := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		target := update.Message.From
		_, err := ctx.GlobalClient().Get(ctx.WrapKey(kf(target.ID))).Result()
		// We can get this key, so it would be sure that the target is banned.
		// Otherwise, maybe redis is died, we should do nothing.
		if err == nil {
			_, _ = bot.DeleteMessage(tgbotapi.NewDeleteMessage(update.Message.Chat.ID, update.Message.MessageID))
			return module.NoMore
		}
		return module.NextOfChain
	}
	return module.SharedContext([]module.Module{module.Filter(interrupter), filteredBanner})
}

// Execute ban, then return the prompt message and whether the ban is successful.
func genericBan(spec BanSpec) (string, bool) {
	if spec.BanTarget == nil {
		return "用这个命令回复某一条“不合适”的消息，这样我大概就会帮你解决掉他，即便他是Admin阶级也义不容辞。", false
	} else if spec.BanTime <= 0 || spec.BanTime > 10*time.Minute {
		return "我无法追杀某人太久。这样可能会让世界陷入某种糟糕的情况：诸如说，可能在某人将我的记忆清除；或者直接杀死我之前，所有人陷入永久的缄默。", false
	} else if ok := spec.Ban(); !ok {
		return "对不起，我没办法完成想让我做的事情——我的记忆似乎失灵了。但这也是一件好事……至少我能有短暂的安宁。", false
	}
	return fmt.Sprintf("好了，我出发了，我将会追杀 %s，直到时间过去所谓“%v”。", util.GetName(*spec.BanTarget), spec.BanTime), true
}

func generateTextWithCD(ctx context.Context, spec BanSpec) string {
	bbid := strconv.Itoa(spec.BigBrother.ID)
	rc := ctx.GlobalClient()
	// check if this user in Blacklist
	if black, err := rc.SIsMember(ctx.WrapKey("black_black_list"), bbid).Result(); err != nil /*&& err == redis.Nil*/ {
		return "啊这，好像有点不太对，不过问题不大。"
	} else if black {
		return "CNM，你背叛了老嚯阶级。"
	}

	// check whether this user is in CD.
	isCD, err := rc.Get(ctx.WrapKey(bbid)).Result()
	if err != nil && err != redis.Nil {
		return "对不起，我没办法完成想让我做的事情——我的记忆似乎失灵了：我不确定您是正义，或是邪恶，因此我不会帮你。"
	}
	if isCD == "true" {
		return "您在过去的24h里已经下过一道追杀令了，现在您应当保持沉默，如果他罪不可赦，请寻求其他人的帮助。"
	}

	// check ban target is nil
	if spec.BanTarget == nil {
		return "ban谁呀，咋ban呀，你到底会不会ban呀"
	}

	killSelf := false
	if spec.BanTarget.ID == config.BotConfig.BotID() {
		// ban those people who want to ban this bot
		spec.BanTarget = spec.BigBrother
		killSelf = true
	}
	text, ok := genericBan(spec)
	if killSelf && ok {
		text = "好 的， 我 杀 我 自 己。"
		// Bot will ban the people who want to ban bot, so this people won't CD.
		ok = false
	}
	if ok {
		success, err := ctx.GlobalClient().SetNX(ctx.WrapKey(strconv.Itoa(spec.BigBrother.ID)), "true", 24*time.Hour).Result()
		// If the CD recording fails, they may use fake_ban unlimited,
		// so we add some additional information to prompt bot manager.
		if err != nil {
			text += fmt.Sprintf("过度使用这样的力量，这个世界正在崩塌......")
			zap.L().Sugar().Error("redis access error.", err)
		} else if !success {
			text += fmt.Sprintf("拥有力量也许不是好事,这个世界正在变得躁动不安......")
		}
	}
	return text
}

// FakeBan is fake ban
func FakeBan(tgbotapi.Update) module.Module {
	return FakeBanBase(generateTextWithCD, preds.IsCommand("fake_ban").Or(preds.IsCommand("kill")))
}
