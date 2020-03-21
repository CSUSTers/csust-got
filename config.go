package csust_got

import (
	"bufio"
	"errors"
	"os"
	"path"
)

// Config the interface for common configs.
type Config struct {
	Token string
}

// FromFolder creates a config from a config folder.
func FromFolder(folder string) (*Config, error) {
	tokenName := path.Join(folder, ".token")
	tokenFile, err := os.Open(tokenName)
	if err != nil {
		return nil, err
	}
	tokenReader := bufio.NewReader(tokenFile)
	tokenBytes, isPrefix, err := tokenReader.ReadLine()
	if err != nil {
		return nil, err
	}
	if isPrefix {
		return nil, errors.New(".token file too long to read")
	}
	conf := &Config{
		Token: string(tokenBytes),
	}
	return conf, nil
}
