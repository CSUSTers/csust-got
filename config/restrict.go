package config

type restrictConfig struct {
	KillSeconds          int `koanf:"kill_duration"`
	FakeBanMaxAddSeconds int `koanf:"fake_ban_max_add"`
}

func (c *restrictConfig) checkConfig() {
	if c.KillSeconds <= 0 {
		c.KillSeconds = 30
	}
	if c.FakeBanMaxAddSeconds <= 0 {
		c.FakeBanMaxAddSeconds = c.KillSeconds / 5
	}
}

type rateLimitConfig struct {
	MaxToken    int     `koanf:"max_token"`
	Limit       float64 `koanf:"limit"`
	Cost        int     `koanf:"cost"`
	StickerCost int     `koanf:"cost_sticker"`
	CommandCost int     `koanf:"cost_command"`
}

func (c *rateLimitConfig) checkConfig() {
	if c.MaxToken <= 0 {
		c.MaxToken = 1
	}
	if c.Limit <= 0 {
		c.Limit = 1
	}
	if c.Cost < 0 {
		c.Cost = 1
	}
	if c.StickerCost < 0 {
		c.StickerCost = 1
	}
	if c.CommandCost < 0 {
		c.CommandCost = 1
	}
}
