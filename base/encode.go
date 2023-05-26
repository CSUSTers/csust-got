package base

import (
	"regexp"
	"strings"

	"csust-got/entities"

	. "gopkg.in/telebot.v3"
)

var (
	// change 'y' to 'i' if end with this.
	yEndTable = [...]string{"ty", "ly", "fy", "py", "dy", "by"}

	// hugeXer regex
	hugeRegex = regexp.MustCompile(`^(huge)+.+(er)+$`)
)

// HugeEncoder encode 'xxx' to 'hugexxxer'.
func HugeEncoder(ctx Context) error {
	arg, ok := parseHugeArgs(ctx)
	if !ok {
		return ctx.Reply(arg, ModeMarkdownV2)
	}

	// encode
	arg = hooEncode(arg)

	return ctx.Reply(arg, ModeMarkdownV2)
}

// HugeDecoder decode 'hugehugehugexxxererer' to 'hugexxxer'.
func HugeDecoder(ctx Context) error {
	arg, ok := parseHugeArgs(ctx)
	if !ok {
		return ctx.Reply(arg, ModeMarkdownV2)
	}

	// decode
	arg = hooDecode(arg)

	return ctx.Reply(arg, ModeMarkdownV2)
}

func parseHugeArgs(ctx Context) (arg string, ok bool) {
	if ctx.Message().ReplyTo != nil {
		arg = ctx.Message().ReplyTo.Text
	}

	command := entities.FromMessage(ctx.Message())

	if command.Argc() > 0 {
		arg = command.ArgAllInOneFrom(0)
	}

	arg = strings.TrimSpace(arg)

	// no args
	if arg == "" {
		return "HUGEFIVER", false
	}

	// tldr
	if len(arg) > 128 {
		return "hugeTLDRer", false
	}

	return arg, true
}

func hooEncode(arg string) string {
	if arg == "" {
		return "HUGEFIVER"
	}
	// add 'huge' to prefix
	if !strings.HasPrefix(arg, "huge") {
		if arg[0] != 'e' {
			arg = "e" + arg
		}
		arg = "hug" + arg
	}
	// add 'er' to suffix
	if !strings.HasSuffix(arg, "er") {
		arg = encodeParseEnd(arg)
		// only add 'r' if $arg end with 'e'
		if arg[len(arg)-1] != 'e' {
			arg += "e"
		}
		arg += "r"
	}
	// if we get 'huger' after encode, we <fork> him.
	if arg == "huger" {
		arg = "hugeF**Ker"
	}

	return arg
}

// encodeParseEnd change 'y' to 'i' if end with $yEndTable
func encodeParseEnd(arg string) string {
	for _, v := range yEndTable {
		if strings.HasSuffix(arg, v) {
			arg = arg[0:len(arg)-1] + "i"
			break
		}
	}
	return arg
}

func hooDecode(arg string) string {
	if !hugeRegex.MatchString(arg) {
		return "hugeFAKEr"
	}

	// find first 'huge' and last 'er'
	huge := strings.Index(arg, "huge")
	er := strings.LastIndex(arg, "er")

	// find end of first consecutive 'huge' and start of last consecutive 'er'
	var hugeEnd, erStart int
	for hugeEnd = huge; hugeEnd+4 < len(arg); hugeEnd += 4 {
		if arg[hugeEnd:hugeEnd+4] != "huge" {
			break
		}
	}
	for erStart = er; erStart-2 >= 0; erStart -= 2 {
		if arg[erStart-2:erStart] != "er" {
			break
		}
	}

	// decode
	arg = arg[0:huge+4] + arg[hugeEnd:erStart] + arg[er:]
	return arg
}
