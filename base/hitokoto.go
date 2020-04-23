package base

import (
    "csust-got/orm"
    "encoding/json"
    "fmt"
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "go.uber.org/zap"
    "io/ioutil"
    "net/http"
    "net/url"
)

type HitokotoResponse struct {
    ID       int    `json:"id"`
    Sentence string `json:"hitokoto"`
    Author   string `json:"from_who"`
    From     string `json:"from"`
}

var HitokotoApi, _ = url.Parse("https://v1.hitokoto.cn/")

var Hitokoto = mapToHTML(func(message *tgbotapi.Message) string {
    resp, err := http.Get(HitokotoApi.String())
    var word []byte
    if err != nil {
        zap.L().Error("Err@Hitokoto [CONNECT TO REMOTE HOST]", zap.Error(err))
        word = loadFromRedis()
        if len(word) == 0 {
            return errMessage
        }
    }
    defer resp.Body.Close()
    word, err = ioutil.ReadAll(resp.Body)
    if err != nil {
        zap.L().Error("Err@Hitokoto [READ FROM HTTP]", zap.Error(err), zap.String("response", fmt.Sprintf("%#v", resp)))
        word = loadFromRedis()
        if len(word) == 0 {
            return errMessage
        }
    }
    koto := HitokotoResponse{}
    err = json.Unmarshal(word, &koto)
    if err != nil {
        zap.L().Error("Err@Hitokoto [JSON PARSE]", zap.Error(err), zap.ByteString("json", word))
        word = loadFromRedis()
        if len(word) == 0 {
            return errMessage
        }
    }
    storeToRedis(word)
    if koto.Author == "" {
        koto.Author = "佚名"
    }
    if koto.From == "" {
        koto.From = "未知出处"
    } else {
        koto.From = "《" + koto.From + "》"
    }

    return fmt.Sprintf("%s by <em>%s %s</em>", koto.Sentence, koto.Author, koto.From)
})


func storeToRedis(respBody []byte) {
    _, err := orm.GetClient().SAdd("hitokoto", string(respBody)).Result()
    if err != nil {
        zap.L().Error("Err@Hotokoto [STORE]", zap.Error(err))
    }
}

func loadFromRedis() []byte {
    res, err := orm.GetClient().SRandMember("hitokoto").Result()
    if err != nil {
        zap.L().Error("Err@Hotokoto [STORE]", zap.Error(err))
        return nil
    }
    return []byte(res)
}