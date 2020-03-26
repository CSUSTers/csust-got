package base

import (
    "csust-got/context"
    "csust-got/util"
    "fmt"
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

func Evaluating(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
    message := update.Message
    text := message.CommandArguments()
    result, err := ctx.EvalCEL(text, message)
    reply := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprint(result))
    reply.ReplyToMessageID = message.MessageID
    reply.ParseMode = "markdown"
    if err != nil {
        reply.Text = fmt.Sprintf("我没办法做这种事。\n```\n%s\n```", err)
    }
    go util.SendMessage(bot, reply)
}