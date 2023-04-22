package config

type specialListConfig struct {
	Name    string  `koanf:"-"`
	Enabled bool    `koanf:"enabled"`
	Chats   []int64 `koanf:"-"`
}

func (c *specialListConfig) checkConfig() {

}

func (c *specialListConfig) SetName(name string) {
	c.Name = name
}

func (c *specialListConfig) Check(chatID int64) bool {
	for _, v := range c.Chats {
		if v == chatID {
			return true
		}
	}
	return false
}
