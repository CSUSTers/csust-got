package config

type promConfig struct {
	Enabled      bool   `koanf:"enabled"`
	Address      string `koanf:"address"`
	MessageQuery string `koanf:"message_query"`
	StickerQuery string `koanf:"sticker_query"`
}

func (c *promConfig) checkConfig() {

}
