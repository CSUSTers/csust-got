package base

import (
	"csust-got/orm"
	"csust-got/util"

	. "gopkg.in/tucnak/telebot.v2"
)

// var (
// 	codeWaiting     = -1  // 打卡尚未开始
// 	codeOK          = 0   // 打卡成功
// 	codeNotFound    = 404 // 用户不存在
// 	codeServerError = 555 // 服务器打卡错误（可能是表单改变或者其他问题）
// )

// type yibanResp struct {
// 	Data    string `json:"data"`
// 	Msg     string `json:"msg"`
// 	ErrCode int    `json:"errCode"`
// }

// var phoneMatcher *regexp.Regexp

// func init() {
// 	phoneMatcher = regexp.MustCompile(`^(1\d{10})$`)
// }

// Yiban is handler for command `/yiban`
func Yiban(m *Message) {

	util.SendMessage(m.Sender, "亦之打卡已于3月19日停止服务，此命令已废弃，将于下个版本关闭")

	// cmd := entities.FromMessage(m)
	// tel := cmd.Arg(0)
	// if tel != "" {
	// 	if !checkTel(tel) {
	// 		util.SendMessage(m.Chat, "bot觉得这不是手机号")
	// 		return
	// 	}
	// 	util.SendMessage(m.Chat, requestAndGetMsg(tel), NoPreview)
	// 	return
	// }
	// tel = orm.GetYiban(m.Sender.ID)
	// if tel == "" {
	// 	util.SendMessage(m.Chat, "亦之的群友特供版自动打卡推送\n不知道你的易班手机号是什么呢~\n命令带上手机号参数可以进行一次性查询，bot不保存您的手机号")
	// 	util.SendMessage(m.Chat, "如果需要bot记住您手机号，请使用 /sub_yiban 命令带上手机号参数，后续查询 /yiban 不再需要填写参数，每日打卡结果bot会推送通知")
	// 	return
	// }
	// util.SendMessage(m.Chat, requestAndGetMsg(tel), NoPreview)
	// orm.YibanNotified(m.Sender.ID)
}

// SubYiban is handler for command `/sub_yiban`
func SubYiban(m *Message) {

	util.SendMessage(m.Sender, "亦之打卡已于3月19日停止服务，此命令已废弃，将于下个版本关闭")

	// cmd := entities.FromMessage(m)
	// tel := cmd.Arg(0)
	// if tel == "" {
	// 	util.SendMessage(m.Chat, "请在命令参数中填写您的手机号")
	// 	return
	// }
	// if !checkTel(tel) {
	// 	util.SendMessage(m.Chat, "bot觉得这不是手机号")
	// 	return
	// }
	// resp := requestYiban(tel)
	// if resp == nil {
	// 	util.SendMessage(m.Chat, "bot请求易班失败 x_x")
	// 	return
	// }
	// if resp.ErrCode == codeWaiting || resp.ErrCode == codeOK {
	// 	if orm.RegisterYiban(m.Sender.ID, tel) {
	// 		util.SendMessage(m.Chat, "bot记下了~以后查询 /yiban 不再需要填写参数，每日打卡结果bot会推送通知，如需bot忘记手机号请使用 /no_yiban")
	// 		return
	// 	}
	// 	util.SendMessage(m.Chat, "bot出了点问题，没能记住您的手机号")
	// 	return
	// }
	// util.SendMessage(m.Chat, getMsg(resp))
}

// NoYiban is handler for command `/no_yiban`
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
	util.SendMessage(m.Sender, "亦之打卡已于3月19日停止服务，此命令将于下个版本关闭")
}

// YibanService is yiban service
func YibanService() {
	// for range time.Tick(60 * time.Minute) {
	mp := orm.GetAllYiban()
	for userID := range mp {
		orm.DelYiban(userID)
		util.SendMessage(&User{ID: userID}, "亦之打卡已于3月19日停止服务，bot已将您的手机号删除，请注意手动打卡")

		// if orm.IsYibanNotified(userID) {
		// 	continue
		// }
		// chat := &Chat{ID: int64(userID)}
		// resp := requestYiban(tel)
		// if resp == nil {
		// 	continue
		// }
		// switch resp.ErrCode {
		// case codeWaiting:
		// 	// keep waiting
		// case codeOK:
		// 	log.Info("yiban service send notify", zap.Int("userID", userID))
		// 	util.SendMessage(chat, getMsg(resp), NoPreview)
		// 	orm.YibanNotified(userID)
		// case codeNotFound:
		// 	log.Warn("yiban service user not found, try delete")
		// 	orm.DelYiban(userID)
		// case codeServerError:
		// 	if !orm.IsYibanFailedNotified(userID) {
		// 		log.Warn("yiban service error", zap.Any("response", *resp))
		// 		util.SendMessage(chat, getMsg(resp))
		// 		orm.YibanFailedNotified(userID)
		// 	}
		// }
	}
	// }
}

// func checkTel(tel string) bool {
// 	return phoneMatcher.MatchString(tel)
// }

// func requestAndGetMsg(tel string) string {
// 	return getMsg(requestYiban(tel))
// }

// func getMsg(resp *yibanResp) string {
// 	if resp == nil {
// 		return "似乎出了点问题呢"
// 	}

// 	switch resp.ErrCode {
// 	case codeWaiting:
// 		return "别急，自动打卡时间还没到呐~"
// 	case codeOK:
// 		return "好耶，自动打卡成功辣~\n" + resp.Data
// 	case codeNotFound:
// 		return "您尚未注册群友特供版~"
// 	case codeServerError:
// 		if len(resp.Msg) > 0 {
// 			return resp.Msg
// 		}
// 		return "打卡失败，可能是表单有变动或服务器异常"
// 	}

// 	return "bot也不知道发生了什么"
// }

// func requestYiban(tel string) *yibanResp {
// 	rsp, err := http.Get(config.BotConfig.YibanAPI + tel)
// 	if err != nil {
// 		log.Error("request yiban failed.", zap.Error(err))
// 		return nil
// 	}
// 	defer func() {
// 		if err = rsp.Body.Close(); err != nil {
// 			log.Error("close response body failed", zap.Error(err))
// 		}
// 	}()

// 	bytes, err := ioutil.ReadAll(rsp.Body)
// 	if err != nil {
// 		log.Error("request yiban read yibanResp failed.", zap.Error(err))
// 		return nil
// 	}

// 	resp := new(yibanResp)
// 	err = json.Unmarshal(bytes, resp)
// 	if err != nil {
// 		log.Error("request yiban unmarshal yibanResp failed.", zap.Error(err))
// 		return nil
// 	}
// 	return resp
// }
