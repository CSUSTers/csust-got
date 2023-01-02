package sd

import . "gopkg.in/telebot.v3"

// StableDiffusionContext is the context of stable diffusion worker.
type StableDiffusionContext struct {
	BotContext Context
	UserConfig StableDiffusionConfig
	Request    StableDiffusionReq
}
