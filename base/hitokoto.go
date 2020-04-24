package base

import (
	"csust-got/command"
	"csust-got/orm"
	"encoding/json"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
)

type HitokotoResponse struct {
	ID       int    `json:"id"`
	Sentence string `json:"hitokoto"`
	Author   string `json:"from_who"`
	From     string `json:"from"`
}

func hitokotoApi() *url.URL {
	u, _ := url.Parse("https://v1.hitokoto.cn/")
	return u
}

type hitokotoArg string

func emptyArg() hitokotoArg {
	return ""
}
func (arg hitokotoArg) toURL() *url.URL {
	q := url.Values{}
	for _, b := range arg {
		q.Add("c", string(b))
	}
	u := hitokotoApi()
	u.RawQuery = q.Encode()
	return u
}

/*
get args from message,
pass args to api to get the specified type of sentence
a -> cartoon
b -> caricature
c -> games
d -> literature
e -> original
f -> from the web
g -> unknown
h -> video
i -> poetry
j -> Netease Music
k -> philosophy
l -> joke
if arg not in above, we will ignore it.
if there is no args, api will randomly choice from above.
if there is multiple args, api will randomly choice from them.
*/
func parseApi(message *tgbotapi.Message) hitokotoArg {
	cmd, _ := command.FromMessage(message)
	cmdSlice := cmd.MultiArgsFrom(0)
	if len(cmdSlice) > 15 {
		return emptyArg()
	}
	return hitokotoArg(strings.Join(cmdSlice, ""))
}

var Hitokoto = mapToHTML(func(message *tgbotapi.Message) string {
	arg := parseApi(message)
	return hitokoto(arg)
})

var HitDawu = mapToHTML(func(*tgbotapi.Message) string {
	return hitokoto("i")
})

func hitokoto(arg hitokotoArg) string {
	u := arg.toURL()
	zap.L().Debug("getting", zap.Stringer("url", u))
	resp, err := http.Get(u.String())
	if err != nil {
		zap.L().Error("Err@Hitokoto [CONNECT TO REMOTE HOST]", zap.Error(err))
		str := loadFromRedis()
		if len(str) == 0 {
			return errMessage
		}
		return str
	}
	defer resp.Body.Close()
	word, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		zap.L().Error("Err@Hitokoto [READ FROM HTTP]", zap.Error(err), zap.String("response", fmt.Sprintf("%#v", resp)))
		str := loadFromRedis()
		if len(str) == 0 {
			return errMessage
		}
		return str
	}
	koto := HitokotoResponse{}
	err = json.Unmarshal(word, &koto)
	if err != nil {
		zap.L().Error("Err@Hitokoto [JSON PARSE]", zap.Error(err), zap.ByteString("json", word))
		str := loadFromRedis()
		if len(str) == 0 {
			return errMessage
		}
		return str
	}
	if koto.Author == "" {
		koto.Author = "佚名"
	}
	if koto.From == "" {
		koto.From = "未知出处"
	} else {
		koto.From = "《" + koto.From + "》"
	}
	str := fmt.Sprintf("%s by <em>%s %s</em>", koto.Sentence, koto.Author, koto.From)
	storeToRedis(str)
	return str
}

func storeToRedis(respBody string) {
	_, err := orm.GetClient().SAdd("hitokoto", respBody).Result()
	if err != nil {
		zap.L().Error("Err@Hotokoto [STORE]", zap.Error(err))
	}
}

func loadFromRedis() string {
	res, err := orm.GetClient().SRandMember("hitokoto").Result()
	if err != nil {
		zap.L().Error("Err@Hotokoto [STORE]", zap.Error(err))
		return ""
	}
	return res
}
