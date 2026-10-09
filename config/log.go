package config

import "github.com/spf13/viper"

const (
	defaultLogMaxSizeMB  = 100
	defaultLogMaxBackups = 7
	defaultLogMaxAgeDays = 14
)

// LogConfig controls rotation of the got.log / got_err.log file outputs.
type LogConfig struct {
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   *bool
}

func (c *LogConfig) readConfig() {
	c.MaxSizeMB = viper.GetInt("log.max_size_mb")
	c.MaxBackups = viper.GetInt("log.max_backups")
	c.MaxAgeDays = viper.GetInt("log.max_age_days")
	if viper.IsSet("log.compress") {
		compress := viper.GetBool("log.compress")
		c.Compress = &compress
	}
}

func (c *LogConfig) checkConfig() {
	if c.MaxSizeMB <= 0 {
		c.MaxSizeMB = defaultLogMaxSizeMB
	}
	if c.MaxBackups <= 0 {
		c.MaxBackups = defaultLogMaxBackups
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = defaultLogMaxAgeDays
	}
}

// CompressEnabled reports whether rotated log files are gzip-compressed (default true).
func (c *LogConfig) CompressEnabled() bool {
	return c == nil || c.Compress == nil || *c.Compress
}
