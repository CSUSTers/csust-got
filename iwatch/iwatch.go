package iwatch

import (
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
		return ctx.Reply("Your watching product:\n%s\n\nYour watching store:%s\n", strings.Join(products, "\n"), strings.Join(stores, "\n"))
	}

	// arg >= 2
	cmdType := command.Arg(0)
	args := command.MultiArgsFrom(1)

	switch cmdType {
	case "p", "prod", "product":
		// register product
		log.Info("register prod", zap.Int64("user", userID), zap.String("list", strings.Join(args, ",")))
		if orm.WatchProduct(userID, args) {
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	case "s", "store":
		// register store
		log.Info("register store", zap.Int64("user", userID), zap.String("list", strings.Join(args, ",")))
		if orm.WatchStore(userID, args) {
			return ctx.Reply("success")
		}
		return ctx.Reply("failed")
	default:
		return ctx.Reply("iwatch <product|store> <prod1|store1> <prod2|store2> ...")
	}
}

// WatchService Apple Store watcher service
func WatchService() {
	for range time.Tick(30 * time.Second) {
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

	if len(resp.Body.Content.PickupMessage.Stores) == 0 {
		return nil, errors.New("no Store")
	}

	pdi, ok := resp.Body.Content.DeliveryMessage[product]
	if !ok {
		return nil, errors.New("resp not found product")
	}

	pd, ok := pdi.(productInfo)
	if !ok {
		return nil, errors.New("resp product wrong type")
	}

	if len(pd.DeliveryOptions) == 0 {
		return nil, errors.New("no DeliveryOptions")
	}

	r := &result{
		Avaliable: false,
		StoreName: resp.Body.Content.PickupMessage.Stores[0].StoreName,
		Date:      pd.DeliveryOptions[0].Date,
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
}

type storeInfo struct {
	StoreName string `json:"storeName"`
}

type productInfo struct {
	DeliveryOptions []struct {
		Date string
	} `json:"deliveryOptions"`
}

type result struct {
	Avaliable bool
	StoreName string
	Date      string
}
