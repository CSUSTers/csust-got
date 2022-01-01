package base

import (
	"regexp"
	"strings"

	"csust-got/entities"

	. "gopkg.in/tucnak/telebot.v3"
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
		return ctx.Reply(arg)
	}

	// encode
	arg = encode(arg)

	// if we get 'huger' after encode, we <fork> him.
	if arg == "huger" {
		arg = "hugeF**Ker"
	}

	return ctx.Reply(arg)
}

// HugeDecoder decode 'hugehugehugexxxererer' to 'hugexxxer'.
func HugeDecoder(ctx Context) error {
	arg, ok := parseHugeArgs(ctx)
	if !ok {
		return ctx.Reply(arg)
	}

	// decode
	arg = decode(arg)

	return ctx.Reply(arg)
}

func parseHugeArgs(ctx Context) (arg string, ok bool) {
	command := entities.FromMessage(ctx.Message())

	// no args
	if command.Argc() <= 0 {
		return "HUGEFIVER", false
	}

	arg = command.ArgAllInOneFrom(0)

	// tldr
	if len(arg) > 128 {
		return "hugeTLDRer", false
	}

	return arg, true
}

func encode(arg string) string {
	// add 'huge' to prefix
	if !strings.HasPrefix(arg, "huge") {
		if arg[0] == 'e' {
			arg = "hug" + arg
		} else {
			arg = "huge" + arg
		}
	}
	// add 'er' to suffix
	if !strings.HasSuffix(arg, "er") {
		// change 'y' to 'i' if end with $xyTable
		for _, v := range yEndTable {
			if strings.HasSuffix(arg, v) {
				arg = arg[0:len(arg)-1] + "i"
				break
			}
		}
		// only add 'r' if $arg end with 'e'
		if arg[len(arg)-1] == 'e' {
			arg += "r"
		} else {
			arg += "er"
		}
	}
	return arg
}

func decode(arg string) string {
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

	// if we will get 'huger', we reply <fork>.
	if erStart < hugeEnd {
		return "hugeF**Ker"
	}

	// decode
	arg = arg[0:huge+4] + arg[hugeEnd:erStart] + arg[er:]
	return arg
}
