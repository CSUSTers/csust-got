package base

import (
	"csust-got/entities"
	"regexp"

	. "gopkg.in/telebot.v3"
)

var (
	hooPrePatt = regexp.MustCompile(`(?i)^h[o0]*`)
	hooSufPatt = regexp.MustCompile(`(?i)[o0]*$`)
)

// HooEncoder encode 'XXX' to 'hooXXXoo'.
func HooEncoder(ctx Context) error {
	_, s, err := entities.CommandTakeArgs(ctx.Message(), 0)
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

	if i1 >= i2-1 {
		return "h0oOo"
	}

	return "h0o" + s[i1:i2] + "Oo"
}
