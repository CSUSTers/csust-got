package base

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"

	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v3"
)

// HitokotoResponse is HitokotoResponse.
type HitokotoResponse struct {
	ID       int    `json:"id"`
	Sentence string `json:"hitokoto"`
	Author   string `json:"from_who"`
	From     string `json:"from"`
}

func (r *HitokotoResponse) setDefault() {
	if r.Author == "" {
		r.Author = "佚名"
	}
	if r.From == "" {
		r.From = "未知出处"
	} else {
		r.From = "《" + r.From + "》"
	}
}

func (r *HitokotoResponse) get(withSource bool) string {
	r.setDefault()
	str := r.Sentence
	if withSource {
		str += fmt.Sprintf(" by <em>%s %s</em>", r.Author, r.From)
		go orm.StoreHitokoto(str)
	}
	return str
}

func hitokotoAPI() *url.URL {
	u, _ := url.Parse("https://v1.hitokoto.cn/")
	return u
}

// HitokotoArg is hitokoto args.
type HitokotoArg string

// HitokotoEmptyArg is hitokoto empty args.
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
func parseAPI(ctx Context) HitokotoArg {
	cmd := entities.FromMessage(ctx.Message())
	cmdSlice := cmd.MultiArgsFrom(0)
	if len(cmdSlice) > 15 {
		return HitokotoEmptyArg()
	}
	return HitokotoArg(strings.Join(cmdSlice, ""))
}

// Hitokoto is command `hitokoto`.
func Hitokoto(ctx Context) error {
	return ctx.Reply(GetHitokoto(parseAPI(ctx), true), ModeHTML)
}

// HitDawu is command alias `hitokoto -i`.
func HitDawu(ctx Context) error {
	return ctx.Reply(GetHitokoto("i", true), ModeHTML)
}

// HitoNetease is command alias `hitokoto -j`.
func HitoNetease(ctx Context) error {
	return ctx.Reply(GetHitokoto("j", true), ModeHTML)
}

// GetHitokoto can get a hitokoto.
func GetHitokoto(arg HitokotoArg, withSource bool) string {
	u := arg.toURL()
	log.Debug("getting", zap.Stringer("url", u))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		log.Error("Err@Hitokoto new request failed", zap.Error(err))
		return orm.GetHitokoto(withSource)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error("Err@Hitokoto [CONNECT TO REMOTE HOST]", zap.Error(err))
		return orm.GetHitokoto(withSource)
	}
	defer func() {
		if err = resp.Body.Close(); err != nil {
			log.Error("close response body failed", zap.Error(err))
		}
	}()
	word, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("Err@Hitokoto [READ FROM HTTP]", zap.Error(err), zap.String("response", fmt.Sprintf("%#v", resp)))
		return orm.GetHitokoto(withSource)
	}
	koto := HitokotoResponse{}
	err = json.Unmarshal(word, &koto)
	if err != nil {
		log.Error("Err@Hitokoto [JSON PARSE]", zap.Error(err), zap.ByteString("json", word))
		return orm.GetHitokoto(withSource)
	}
	koto.setDefault()
	return koto.get(withSource)
}
