package restrict

import (
	"csust-got/orm"
	"time"

	tb "gopkg.in/telebot.v3"
)

// Kill exec fake ban.
func Kill(chat *tb.Chat, user *tb.User, d time.Duration) Result {
	if !checkKillDuration(d) {
		return fail()
	}

	if orm.AddBanDuration(chat.ID, 0 /* 0 for system */, user.ID, d) {
		banned := orm.GetBannedDuration(chat.ID, user.ID)
		if banned == 0 {
			return fail()
		}
		return success(d, time.Now().Add(banned), true, RestrictTypeKill)
	}
	if orm.Ban(chat.ID, 0 /* 0 for system */, user.ID, d) {
		return success(d, time.Now().Add(d), true, RestrictTypeKill)
	}

	return fail()
}

func checkKillDuration(d time.Duration) bool {
	return d >= 10*time.Second
}
