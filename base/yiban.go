package base

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/json"
	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v2"
	"io/ioutil"
	"net/http"
	"time"
)

var (
	codeWaiting     = -1  // 打卡尚未开始
	codeOK          = 0   // 打卡成功
	codeNotFound    = 404 // 用户不存在
	codeServerError = 555 // 服务器打卡错误（可能是表单改变或者其他问题）
)

type yibanResp struct {
	Data    string `json:"data"`
	Msg     string `json:"msg"`
	ErrCode int    `json:"errCode"`
}

func Yiban(m *Message) {
	cmd := entities.FromMessage(m)
	tel := cmd.Arg(0)
	if tel != "" {
		if !checkTel(tel) {
			util.SendMessage(m.Chat, "bot觉得这不是手机号")
			return
		}
		util.SendMessage(m.Chat, requestAndGetMsg(tel))
		return
	}
	tel = orm.GetYiban(m.Sender.ID)
	if tel == "" {
		util.SendMessage(m.Chat, "不知道你的易班手机号是什么呢~\n命令带上手机号参数可以进行一次性查询，bot不保存您的手机号")
		util.SendMessage(m.Chat, "如果需要bot记住您手机号，请使用 /sub_yiban 命令带上手机号参数，后续查询 /yiban 不再需要填写参数，每日打卡结果bot会推送通知")
		return
	}
	util.SendMessage(m.Chat, requestAndGetMsg(tel))
	// orm.YibanNotified(m.Sender.ID)
}

func SubYiban(m *Message) {
	cmd := entities.FromMessage(m)
	tel := cmd.Arg(0)
	if tel == "" {
		util.SendMessage(m.Chat, "请在命令参数中填写您的手机号")
		return
	}
	if !checkTel(tel) {
		util.SendMessage(m.Chat, "bot觉得这不是手机号")
		return
	}
	resp := requestYiban(tel)
	if resp.ErrCode == codeWaiting || resp.ErrCode == codeOK {
		if orm.RegisterYiban(m.Sender.ID, tel) {
			util.SendMessage(m.Chat, "bot记下了，以后查询 /yiban 不再需要填写参数，每日打卡结果bot会推送通知，如需bot忘记手机号请使用 /no_yiban")
			return
		}
		util.SendMessage(m.Chat, "bot出了点问题，没能记住您的手机号")
		return
	}
	util.SendMessage(m.Chat, getMsg(resp))
}

func NoYiban(m *Message) {
	tel := orm.GetYiban(m.Sender.ID)
	if tel == "" {
		util.SendMessage(m.Chat, "bot不知道您的手机号哦")
		return
	}
	if orm.DelYiban(m.Sender.ID) {
		util.SendMessage(m.Chat, "bot已将您的手机号删除，注意并没有从上游删除，打卡依然会继续进行")
	} else {
		util.SendMessage(m.Chat, "删除失败x_x")
	}
}

func YibanService() {
	for range time.Tick(30 * time.Minute) {
		mp := orm.GetAllYiban()
		for userID, tel := range mp {
			if orm.IsYibanNotified(userID) {
				continue
			}
			chat := &Chat{ID: int64(userID)}
			resp := requestYiban(tel)
			if resp == nil {
				continue
			}
			switch resp.ErrCode {
			case codeWaiting:
				// keep waiting
			case codeOK:
				log.Info("yiban service send notify", zap.Int("userID", userID))
				util.SendMessage(chat, getMsg(resp))
				orm.YibanNotified(userID)
			case codeNotFound:
				log.Warn("yiban service user not found, try delete")
				orm.DelYiban(userID)
			case codeServerError:
				util.SendMessage(chat, getMsg(resp))
			}
		}
	}
}

func checkTel(tel string) bool {
	if len(tel) != 11 {
		return false
	}
	for _, c := range tel {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func requestAndGetMsg(tel string) string {
	return getMsg(requestYiban(tel))
}

func getMsg(resp *yibanResp) string {
	if resp == nil {
		return "似乎出了点问题呢"
	}

	switch resp.ErrCode {
	case codeWaiting:
		return "别急，打卡时间还没到呢"
	case codeOK:
		return "自动打卡成功\n" + resp.Data
	case codeNotFound:
		return "您尚未注册群友特供版"
	case codeServerError:
		return "打卡异常，请联系亦之"
	}

	return "bot也不知道发生了什么"
}

func requestYiban(tel string) *yibanResp {
	rsp, err := http.Get(config.BotConfig.YibanApi + tel)
	if err != nil {
		log.Error("request yiban failed.")
		return nil
	}
	defer rsp.Body.Close()

	bytes, err := ioutil.ReadAll(rsp.Body)
	if err != nil {
		log.Error("request yiban read yibanResp failed.")
		return nil
	}

	resp := new(yibanResp)
	err = json.Unmarshal(bytes, resp)
	if err != nil {
		log.Error("request yiban unmarshal yibanResp failed.")
		return nil
	}
	return resp
}
