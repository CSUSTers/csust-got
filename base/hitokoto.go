package base

import (
	"csust-got/entities"
	"csust-got/orm"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

const errMessage = `过去那些零碎的细语并不构成这个世界：对于你而言，该看，该想，该体会身边那些微小事物的律动。
忘了这些话吧。忘了这个功能吧——只今它已然不能给予你更多。而你的未来属于新的旅途：去欲望、去收获、去爱、去恨。
去做只属于你自己的选择，写下只有你深谙个中滋味的诗篇。我们的生命以后可能还会交织之时，但如今，再见辣。`

// HitokotoResponse is HitokotoResponse
type HitokotoResponse struct {
	ID       int    `json:"id"`
	Sentence string `json:"hitokoto"`
	Author   string `json:"from_who"`
	From     string `json:"from"`
}

func hitokotoAPI() *url.URL {
	u, _ := url.Parse("https://v1.hitokoto.cn/")
	return u
}

// HitokotoArg is hitokoto args
type HitokotoArg string

// HitokotoEmptyArg is hitokoto empty args
func HitokotoEmptyArg() HitokotoArg {
	return ""
}

func (arg HitokotoArg) toURL() *url.URL {
	q := url.Values{}
	for _, b := range arg {
		q.Add("c", string(b))
	}
	u := hitokotoAPI()
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
func parseAPI(message *tgbotapi.Message) HitokotoArg {
	cmd, _ := entities.FromMessage(message)
	cmdSlice := cmd.MultiArgsFrom(0)
	if len(cmdSlice) > 15 {
		return HitokotoEmptyArg()
	}
	return HitokotoArg(strings.Join(cmdSlice, ""))
}

// Hitokoto is command `hitokoto`
var Hitokoto = mapToHTML(func(message *tgbotapi.Message) string {
	arg := parseAPI(message)
	return GetHitokoto(arg, true)
})

// HitDawu is command alias `hitokoto -i`
var HitDawu = mapToHTML(func(*tgbotapi.Message) string {
	return GetHitokoto("i", true)
})

// HitoNetease is command alias `hitokoto -j`
var HitoNetease = mapToHTML(func(*tgbotapi.Message) string {
	return GetHitokoto("j", true)
})

// GetHitokoto can get a hitokoto
func GetHitokoto(arg HitokotoArg, from bool) string {
	u := arg.toURL()
	zap.L().Debug("getting", zap.Stringer("url", u))
	resp, err := http.Get(u.String())
	if err != nil {
		zap.L().Error("Err@Hitokoto [CONNECT TO REMOTE HOST]", zap.Error(err))
		return loadFromRedis(from)
	}
	defer resp.Body.Close()
	word, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		zap.L().Error("Err@Hitokoto [READ FROM HTTP]", zap.Error(err), zap.String("response", fmt.Sprintf("%#v", resp)))
		return loadFromRedis(from)
	}
	koto := HitokotoResponse{}
	err = json.Unmarshal(word, &koto)
	if err != nil {
		zap.L().Error("Err@Hitokoto [JSON PARSE]", zap.Error(err), zap.ByteString("json", word))
		return loadFromRedis(from)
	}
	if koto.Author == "" {
		koto.Author = "佚名"
	}
	if koto.From == "" {
		koto.From = "未知出处"
	} else {
		koto.From = "《" + koto.From + "》"
	}
	str := fmt.Sprintf("%s ", koto.Sentence)
	if from {
		str += fmt.Sprintf("by <em>%s %s</em>", koto.Author, koto.From)
		storeToRedis(str)
	}
	return str
}

func storeToRedis(respBody string) {
	_, err := orm.GetClient().SAdd("hitokoto", respBody).Result()
	if err != nil {
		zap.L().Error("Err@Hitokoto [STORE]", zap.Error(err))
	}
}

func loadFromRedis(from bool) string {
	res, err := orm.GetClient().SRandMember("hitokoto").Result()
	if err != nil {
		zap.L().Error("Err@Hitokoto [STORE]", zap.Error(err))
		return errMessage
	}
	if !from {
		res = res[:strings.LastIndex(res, " by ")+1]
	}
	return res
}
