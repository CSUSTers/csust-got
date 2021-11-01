package iwatch

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v3"
)

// WatchHandler Apple Store Handler
func WatchHandler(ctx Context) error {
	command := entities.FromMessage(ctx.Message())

	// invalid command
	if command.Argc() == 1 {
		return ctx.Reply("watch what?")
	}

	userID := ctx.Sender().ID

	// list
	if command.Argc() == 0 {
		products, productsOK := orm.GetWatchingProducts(userID)
		stores, storesOK := orm.GetWatchingStores(userID)
		if !productsOK || !storesOK {
			return ctx.Reply("failed")
		}
		for i, v := range products {
			products[i] = fmt.Sprintf("[%s] %s", v, orm.GetProductName(v))
		}
		for i, v := range stores {
			stores[i] = fmt.Sprintf("[%s] %s", v, orm.GetStoreName(v))
		}
		return ctx.Reply(fmt.Sprintf("Your watching products:\n%s\n\nYour watching stores:\n%s\n", strings.Join(products, "\n"), strings.Join(stores, "\n")))
	}

	// arg >= 2
	cmdType := command.Arg(0)
	args := command.MultiArgsFrom(1)
	cmdType = strings.ToLower(cmdType)
	for i, v := range args {
		args[i] = strings.ToUpper(v)
	}

	switch cmdType {
	case "a", "add", "+":
		// register
		log.Info("register", zap.Int64("user", userID), zap.Any("list", args))
		products := make([]string, 0)
		stores := make([]string, 0)
		for _, v := range args {
			if isProduct(v) {
				products = append(products, v)
			}
			if isStore(v) {
				stores = append(stores, v)
			}
		}
		if len(products) == 0 && len(stores) == 0 {
			return ctx.Reply("no product/store found")
		}
		if orm.WatchProduct(userID, products) && orm.WatchStore(userID, stores) {
			go updateTargets()
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	case "r", "rm", "remove", "d", "del", "delete", "-":
		// remove store
		log.Info("remove", zap.Int64("user", userID), zap.Any("list", args))
		if orm.RemoveProduct(userID, args) && orm.RemoveStore(userID, args) {
			go removeTargets()
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	default:
		return ctx.Reply("iwatch <add|remove> <prod1|store1> <prod2|store2> ...")
	}
}

var (
	watchingMap  = make(map[string]map[int64]struct{})
	watchingLock = sync.RWMutex{}
)

// WatchService Apple Store watcher service
func WatchService() {
	resultChan := make(chan *result, 1024)
	go watchSender(resultChan)

	for range time.Tick(20 * time.Second) {
		go watchApple(resultChan)
	}
}

// watchSender watching Apple Store watcher send notify
func watchSender(ch <-chan *result) {
	for r := range ch {
		msg := "现在没有货了！"
		if r.Avaliable {
			msg = "有货啦！"
		}
		msg = fmt.Sprintf("%s\n%s\n%s\n", msg, r.ProductName, r.StoreName)
		userList := make([]int64, 0)
		watchingLock.RLock()
		for userID := range watchingMap[r.Product+":"+r.Store] {
			userList = append(userList, userID)
		}
		watchingLock.RUnlock()
		for _, userID := range userList {
			_, err := config.BotConfig.Bot.Send(&User{ID: userID}, msg)
			if err != nil {
				log.Error("send watching msg failed", zap.Int64("user", userID), zap.String("msg", msg))
			}
		}
	}
}

// watchApple Apple Store watcher service
func watchApple(ch chan<- *result) {
	targets, ok := orm.GetTargetList()
	if !ok {
		return
	}

	for _, t := range targets {
		info := strings.Split(t, ":")
		product, store := info[0], info[1]
		r, err := getTargetState(product, store)
		if err != nil {
			log.Error("getTargetState failed", zap.String("product", product), zap.String("store", store), zap.Error(err))
			continue
		}
		log.Info("getTargetState success", zap.String("product", product), zap.String("store", store), zap.Any("result", r))
		if r.ProductName != "" {
			orm.SetProductName(product, r.ProductName)
		}
		if r.StoreName != "" {
			orm.SetStoreName(store, r.StoreName)
		}
		last := orm.GetTargetState(t)
		if r.Avaliable != last {
			ch <- r
		}
		orm.SetTargetState(t, r.Avaliable)
		time.Sleep(500 * time.Millisecond)
	}
}

func getTargetState(product, store string) (*result, error) {
	api := "https://www.apple.com.cn/shop/fulfillment-messages?little=true&mt=regular&parts.0=%s&store=%s"
	api = fmt.Sprintf(api, url.QueryEscape(product), url.QueryEscape(store))

	req, err := http.NewRequest("GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("referer", "https://www.apple.com/shop/buy-iphone")
	req.Header.Set("user-agent", util.RandUA())

	cli := &http.Client{Timeout: 3 * time.Second}
	res, err := cli.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			log.Warn("resp", zap.String("product", product), zap.String("store", store), zap.ByteString("body", body))
		}
	}()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http code is %d", res.StatusCode)
	}

	resp := new(targetResp)
	err = json.Unmarshal(body, resp)
	if err != nil {
		return nil, err
	}

	if resp.Body.Content.PickupMessage.ErrorMessage != "" {
		return nil, errors.New(resp.Body.Content.PickupMessage.ErrorMessage)
	}

	if len(resp.Body.Content.PickupMessage.Stores) == 0 {
		return nil, errors.New("no Store")
	}

	// pdi, ok := resp.Body.Content.DeliveryMessage[product]
	// if !ok {
	// 	return nil, errors.New("resp not found product")
	// }

	// pdb, err := json.Marshal(pdi)
	// if err != nil {
	// 	return nil, errors.New("resp product marshal failed:" + err.Error())
	// }

	// pd := new(productInfo)
	// err = json.Unmarshal(pdb, pd)
	// if err != nil {
	// 	return nil, errors.New("resp product unmarshal failed:" + err.Error())
	// }

	// if len(pd.DeliveryOptions) == 0 {
	// 	return nil, errors.New("no DeliveryOptions")
	// }

	storeResp := resp.Body.Content.PickupMessage.Stores[0]
	if _, ok := storeResp.PartsAvailability[product]; !ok {
		return nil, errors.New("no PartsAvailability")
	}

	r := &result{
		Avaliable:   storeResp.PartsAvailability[product].StoreSelectionEnabled,
		Product:     product,
		Store:       store,
		ProductName: storeResp.PartsAvailability[product].StorePickupProductTitle,
		StoreName:   fmt.Sprintf("%s/%s/%s", storeResp.State, storeResp.City, storeResp.StoreName),
		// Date:        pd.DeliveryOptions[0].Date,
	}

	return r, nil
}

type targetResp struct {
	Head struct {
		Status string
		Data   interface{}
	}

	Body struct {
		Content struct {
			PickupMessage pickupMessage `json:"pickupMessage"`
			// DeliveryMessage map[string]interface{} `json:"deliveryMessage"`
		}
	}
}

type pickupMessage struct {
	Stores         []storeInfo
	PickupLocation string `json:"pickupLocation"`
	ErrorMessage   string `json:"errorMessage"`
}

type storeInfo struct {
	State             string
	City              string
	StoreName         string                       `json:"storeName"`
	PartsAvailability map[string]partsAvailability `json:"partsAvailability"`
}

type partsAvailability struct {
	StorePickEligible       bool   `json:"storePickEligible"`
	StoreSearchEnabled      bool   `json:"storeSearchEnabled"`
	StoreSelectionEnabled   bool   `json:"storeSelectionEnabled"`
	StorePickupQuote        string `json:"storePickupQuote2_0"`
	PickupSearchQuote       string `json:"pickupSearchQuote"`
	StorePickupProductTitle string `json:"storePickupProductTitle"`
	PickupDisplay           string `json:"pickupDisplay"`
	PickupType              string `json:"pickupType"`
}

// type productInfo struct {
// 	DeliveryOptions []struct {
// 		Date string
// 	} `json:"deliveryOptions"`
// }

type result struct {
	Avaliable   bool
	Store       string
	Product     string
	StoreName   string
	ProductName string
	// Date        string
}

func isProduct(product string) bool {
	if len(product) != 9 {
		return false
	}
	if product[7] != '/' {
		return false
	}
	if !util.IsUpper(rune(product[0])) || !util.IsUpper(rune(product[1])) || !util.IsUpper(rune(product[5])) || !util.IsUpper(rune(product[6])) || !util.IsUpper(rune(product[8])) {
		return false
	}
	for _, v := range product[2:5] {
		if !util.IsUpper(v) && !util.IsNumber(v) {
			return false
		}
	}
	return true
}

func isStore(store string) bool {
	if len(store) != 4 {
		return false
	}
	if store[0] != 'R' {
		return false
	}
	for _, v := range store {
		if !util.IsNumber(v) {
			return false
		}
	}
	return true
}

// watching watching Apple Store watcher
func updateTargets() {
	tmpMap, ok := orm.GetTargetMap()
	if !ok {
		log.Warn("update watching map failed")
		return
	}

	watchingLock.Lock()
	watchingMap = tmpMap
	watchingLock.Unlock()
}

// try remove targets
func removeTargets() {
	remTargets := make([]string, 0)
	if tmpMap, ok := orm.GetTargetMap(); ok {
		if targetList, ok := orm.GetTargetList(); ok {
			for _, target := range targetList {
				if tmpMap[target] == nil {
					remTargets = append(remTargets, target)
				}
			}
		}
	}
	log.Info("remove targets", zap.Any("targets", remTargets))
	orm.AppleTargetRemove(remTargets...)
	updateTargets()
}
