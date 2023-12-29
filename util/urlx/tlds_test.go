package urlx

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTldsRegex(t *testing.T) {
	patt := regexp.MustCompile(fmt.Sprintf(`^%[1]s$`, TLDRegex))

	for _, s := range TLDs {
		assert.True(t, patt.MatchString(s), "TLD regex cannot match '%s'", s)
	}
}
