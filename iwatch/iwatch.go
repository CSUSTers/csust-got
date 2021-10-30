package iwatch

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
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
		var products, stores []string
		var productsOK, storesOK bool
		products, productsOK = orm.GetWatchingProducts(userID)
		stores, storesOK = orm.GetWatchingStores(userID)
		if !productsOK || !storesOK {
			return ctx.Reply("failed")
		}
		for i, v := range products {
			products[i] = fmt.Sprintf("[%s] %s", v, orm.GetProductName(v))
		}
		for i, v := range stores {
			stores[i] = fmt.Sprintf("[%s] %s", v, orm.GetStoreName(v))
		}
		return ctx.Reply(fmt.Sprintf("Your watching product:\n%s\n\nYour watching store:\n%s\n", strings.Join(products, "\n"), strings.Join(stores, "\n")))
	}

	// arg >= 2
	cmdType := command.Arg(0)
	args := command.MultiArgsFrom(1)

	switch cmdType {
	case "p", "prod", "product":
		// register product
		log.Info("register prod", zap.Int64("user", userID), zap.Any("list", args))
		if orm.WatchProduct(userID, args) {
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	case "s", "store":
		// register store
		log.Info("register store", zap.Int64("user", userID), zap.Any("list", args))
		if orm.WatchStore(userID, args) {
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	case "r", "rm", "remove":
		// remove store
		log.Info("remove", zap.Int64("user", userID), zap.Any("list", args))
		if orm.RemoveProduct(userID, args) && orm.RemoveStore(userID, args) {
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	default:
		return ctx.Reply("iwatch <product|store|remove> <prod1|store1> <prod2|store2> ...")
	}
}

// WatchService Apple Store watcher service
func WatchService() {
	for range time.Tick(60 * time.Second) {
		go watchApple()
	}
}

// watchApple Apple Store watcher service
func watchApple() {
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
		if r.Avaliable {
			msg := fmt.Sprintf("有货啦!\n%s\n%s\n", r.ProductName, r.StoreName)
			config.BotConfig.Bot.Send(&User{ID: 424901821}, msg)
		}
	}
}

func getTargetState(product, store string) (*result, error) {
	api := "https://www.apple.com.cn/shop/fulfillment-messages?little=true&mt=regular&parts.0=%s&store=%s"
	api = fmt.Sprintf(api, url.QueryEscape(product), url.QueryEscape(store))

	req, err := http.NewRequest("GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("user-agent", "vscode-restclient")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	log.Info("resp", zap.String("product", product), zap.String("store", store), zap.ByteString("body", body))

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

	pdi, ok := resp.Body.Content.DeliveryMessage[product]
	if !ok {
		return nil, errors.New("resp not found product")
	}

	pdb, err := json.Marshal(pdi)
	if err != nil {
		return nil, errors.New("resp product marshal failed:" + err.Error())
	}

	pd := new(productInfo)
	err = json.Unmarshal(pdb, pd)
	if err != nil {
		return nil, errors.New("resp product unmarshal failed:" + err.Error())
	}

	if len(pd.DeliveryOptions) == 0 {
		return nil, errors.New("no DeliveryOptions")
	}

	storeResp := resp.Body.Content.PickupMessage.Stores[0]
	if _, ok := storeResp.PartsAvailability[product]; !ok {
		return nil, errors.New("no PartsAvailability")
	}

	r := &result{
		Avaliable:   storeResp.PartsAvailability[product].StoreSelectionEnabled,
		ProductName: storeResp.PartsAvailability[product].StorePickupProductTitle,
		StoreName:   fmt.Sprintf("%s/%s/%s", storeResp.State, storeResp.City, storeResp.StoreName),
		Date:        pd.DeliveryOptions[0].Date,
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
			PickupMessage   pickupMessage          `json:"pickupMessage"`
			DeliveryMessage map[string]interface{} `json:"deliveryMessage"`
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

type productInfo struct {
	DeliveryOptions []struct {
		Date string
	} `json:"deliveryOptions"`
}

type result struct {
	Avaliable   bool
	StoreName   string
	ProductName string
	Date        string
}
