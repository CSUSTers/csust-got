package base

import (
	"bytes"
	"csust-got/entities"
	"math/rand"
	"regexp"

	. "gopkg.in/telebot.v3"
)

var (
	hooPrePatt = regexp.MustCompile(`(?i)^h[o0]*`)
	hooSufPatt = regexp.MustCompile(`(?i)[o0]*$`)
	hooRunes   = []rune("o0O")
)

// HooEncoder encode 'XXX' to 'hooXXXoo'.
func HooEncoder(ctx Context) error {
	_, s, err := entities.CommandTakeArgs(ctx.Message(), -1)
	if err != nil {
		return ctx.Reply("h0oOo")
	}

	s = hooEncode(s)
	return ctx.Reply(s)
}

func hooEncode(s string) string {
	matches1 := hooPrePatt.FindStringIndex(s)
	matches2 := hooSufPatt.FindStringIndex(s)

	i1, i2 := 0, len(s)
	if matches1 != nil {
		i1 = matches1[1]
	}
	if matches2 != nil {
		i2 = matches2[0]
	}

	bs := bytes.NewBufferString("h")

	if i1 >= i2-1 {
		for i := 0; i < 4; i++ { // Corrected the range to an explicit integer
			bs.WriteRune(hooRunes[rand.Intn(len(hooRunes))]) // Changed rand.N to rand.Intn
		}
		return bs.String()
	}

	for i := 0; i < 2; i++ { // Corrected the range to an explicit integer
		bs.WriteRune(hooRunes[rand.Intn(len(hooRunes))])
	}

	bs.WriteString(s[i1:i2])

	for i := 0; i < 2; i++ { // Corrected the range to an explicit integer
		bs.WriteRune(hooRunes[rand.Intn(len(hooRunes))])
	}

	return bs.String()
}
