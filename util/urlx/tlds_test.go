package urlx

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTldsRegex(t *testing.T) {
	tldsPatt := regexp.MustCompile(fmt.Sprintf(`^%[1]s$`, TLDRegex))
	tldsAsciiPatt := regexp.MustCompile(fmt.Sprintf(`^%[1]s$`, TLDAsciiRegex))

	t.Run("TLDRegex should match all `TLDs`", func(t *testing.T) {
		for _, s := range TLDs {
			assert.True(t, tldsPatt.MatchString(s), "TLD regex cannot match '%s'", s)
		}
	})

	t.Run("TLDAsciiRegex should match all `TLDsAscii`", func(t *testing.T) {
		for _, s := range TLDsAscii {
			assert.True(t, tldsAsciiPatt.MatchString(s), "TLD regex cannot match '%s'", s)
		}
	})
}
