package base

import (
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	. "gopkg.in/tucnak/telebot.v2"

	"go.uber.org/zap"
)

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
func parseAPI(m *Message) HitokotoArg {
	cmd := entities.FromMessage(m)
	cmdSlice := cmd.MultiArgsFrom(0)
	if len(cmdSlice) > 15 {
		return HitokotoEmptyArg()
	}
	return HitokotoArg(strings.Join(cmdSlice, ""))
}

// Hitokoto is command `hitokoto`
func Hitokoto(m *Message) {
	util.SendReply(m.Chat, GetHitokoto(parseAPI(m), true), m, ModeHTML)
}

// HitDawu is command alias `hitokoto -i`
func HitDawu(m *Message) {
	util.SendReply(m.Chat, GetHitokoto("i", true), m, ModeHTML)
}

// HitoNetease is command alias `hitokoto -j`
func HitoNetease(m *Message) {
	util.SendReply(m.Chat, GetHitokoto("j", true), m, ModeHTML)
}

// GetHitokoto can get a hitokoto
func GetHitokoto(arg HitokotoArg, from bool) string {
	u := arg.toURL()
	log.Debug("getting", zap.Stringer("url", u))
	resp, err := http.Get(u.String())
	if err != nil {
		log.Error("Err@Hitokoto [CONNECT TO REMOTE HOST]", zap.Error(err))
		return orm.GetHitokoto(from)
	}
	defer resp.Body.Close()
	word, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("Err@Hitokoto [READ FROM HTTP]", zap.Error(err), zap.String("response", fmt.Sprintf("%#v", resp)))
		return orm.GetHitokoto(from)
	}
	koto := HitokotoResponse{}
	err = json.Unmarshal(word, &koto)
	if err != nil {
		log.Error("Err@Hitokoto [JSON PARSE]", zap.Error(err), zap.ByteString("json", word))
		return orm.GetHitokoto(from)
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
		orm.StoreHitokoto(str)
	}
	return str
}
