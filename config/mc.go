package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type mcConfig struct {
	Mc2Dead int

	// Sacrifices is a list of sacrifices with number of seconds.
	// e.g. [300, 60, 10] means the last on sacrifice will be 300 seconds,
	// and the second on sacrifice will be 60 seconds, and the others will be 10 seconds.
	Sacrifices []int

	// Odds is applied to sacrifice for who `reburn`ed me.
	Odds int

	// Timout means how long nobody call `mc` to cancel context, in seconds
	Timout int
}

func (c *mcConfig) readConfig() {
	c.Mc2Dead = viper.GetInt("mc.max_count")
	c.Mc2Dead = viper.GetInt("mc.mc2dead")

	c.Sacrifices = viper.GetIntSlice("mc.sacrifices")
	c.Odds = viper.GetInt("mc.odds")
	c.Timout = viper.GetInt("mc.timeout")
}

func (c *mcConfig) checkConfig() {
	if c.Mc2Dead > 10 {
		zap.L().Fatal("mc config: `Mc2Dead` must in [0, 10], negative means 0, 0 means off", zap.Int("Mc2Dead", c.Mc2Dead))
	}

	if len(c.Sacrifices) == 0 {
		c.Sacrifices = []int{300, 60}
	}

	for _, sacrifice := range c.Sacrifices {
		if sacrifice < 30 || sacrifice > 600 {
			zap.L().Fatal("mc config: `Sacrifices` must in [30, 600]", zap.Int("Sacrifices", sacrifice))
		}
	}

	if c.Timout <= 0 {
		c.Timout = 24 * 3600
	} else if c.Timout < 60 {
		c.Timout = 60
	}

	if c.Odds <= 1 {
		c.Odds = 2
	}
}
