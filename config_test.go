package csust_got

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"testing"
)

func TestReadFromFolder(c *testing.T) {
	folder, err := ioutil.TempDir("", "got-config-test")
	if err != nil {
		c.Fatal("failed to create temp folder.")
	}
	defer os.RemoveAll(folder)

	tokenFile, err := os.Create(path.Join(folder, ".token"))
	if err != nil {
		c.Fatal("failed to create .token file.")
	}
	token := "MyAwesomeToken"
	_, _ = fmt.Fprintf(tokenFile, token)
	_ = tokenFile.Close()

	config, err := FromFolder(folder)
	if err != nil || config.Token != token {
		c.Fatal("failed to pass the test: failed to make config or config is broken.")
	}
}
