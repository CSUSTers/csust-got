package iwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"

	"go.uber.org/zap"
	"golang.org/x/net/context"
	. "gopkg.in/tucnak/telebot.v3"
)

// iwatch err
var (
	ErrNoStore             = errors.New("no Store")
	ErrNoPartsAvailability = errors.New("no PartsAvailability")
	ErrStatusCode          = errors.New("response status code is not 200")
	ErrResponeErrorMessage = errors.New("response has error message")
)

var (
	productRegex = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[A-Z]{2}/[A-Z]$`)
	storeRegex   = regexp.MustCompile(`R[0-9]{3}`)
)

// WatchHandler Apple Store Handler.
func WatchHandler(ctx Context) error {
	command := entities.FromMessage(ctx.Message())

	// invalid command
	if command.Argc() == 1 {
		return ctx.Reply("watch what?")
	}

	userID := ctx.Sender().ID

	// list
	if command.Argc() == 0 {
		return listCommand(ctx, userID)
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
		return addCommand(ctx, userID, args)
	case "r", "rm", "remove", "d", "del", "delete", "-":
		return remCommand(ctx, userID, args)
	default:
		return ctx.Reply("iwatch <add|remove> <prod1|store1> <prod2|store2> ...")
	}
}

func listCommand(ctx Context, userID int64) error {
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
	return ctx.Reply(fmt.Sprintf("Your watching products:\n%s\n\nYour watching stores:\n%s\n",
		strings.Join(products, "\n"), strings.Join(stores, "\n")))
}

func addCommand(ctx Context, userID int64, args []string) error {
	// register
	log.Info("register", zap.Int64("user", userID), zap.Any("list", args))
	products, stores := splitProductAndStore(args)
	if len(products) == 0 && len(stores) == 0 {
		return ctx.Reply("no product/store found")
	}
	if orm.WatchProduct(userID, products) && orm.WatchStore(userID, stores) {
		go updateTargets()
		return ctx.Reply("success")
	}
	return ctx.Reply("failed")
}

func remCommand(ctx Context, userID int64, args []string) error {
	// remove store
	log.Info("remove", zap.Int64("user", userID), zap.Any("list", args))
	products, stores := splitProductAndStore(args)
	if orm.RemoveProduct(userID, products) && orm.RemoveStore(userID, stores) {
		go removeTargets()
		return ctx.Reply("success")
	}
	return ctx.Reply("failed")
}

func splitProductAndStore(args []string) (products, stores []string) {
	for _, v := range args {
		if isProduct(v) {
			products = append(products, v)
		}
		if isStore(v) {
			stores = append(stores, v)
		}
	}
	return products, stores
}

var (
	watchingMap  = make(map[string]map[int64]struct{})
	watchingLock = sync.RWMutex{}
)

// WatchService Apple Store watcher service.
func WatchService() {
	resultChan := make(chan *result, 1024)
	go watchSender(resultChan)

	for range time.Tick(30 * time.Second) {
		go watchApple(resultChan)
	}
}

// watchSender watching Apple Store watcher send notify.
func watchSender(ch <-chan *result) {
	for r := range ch {
		msg := "现在没有货了!"
		if r.Avaliable {
			msg = "有货啦!"
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

// watchApple Apple Store watcher service.
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
		time.Sleep(time.Second)
	}
}

func getTargetState(product, store string) (*result, error) {
	body, err := requestApple(product, store)
	if err != nil {
		return nil, err
	}

	resp := new(targetResp)
	err = json.Unmarshal(body, resp)
	if err != nil {
		return nil, err
	}

	if resp.Body.Content.PickupMessage.ErrorMessage != "" {
		return nil, fmt.Errorf("%s: %w", resp.Body.Content.PickupMessage.ErrorMessage, ErrResponeErrorMessage)
	}

	if len(resp.Body.Content.PickupMessage.Stores) == 0 {
		return nil, ErrNoStore
	}

	storeResp := resp.Body.Content.PickupMessage.Stores[0]
	if _, ok := storeResp.PartsAvailability[product]; !ok {
		return nil, ErrNoPartsAvailability
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

func requestApple(product, store string) ([]byte, error) {
	api := "https://www.apple.com.cn/shop/fulfillment-messages?little=true&mt=regular&parts.0=%s&store=%s"
	api = fmt.Sprintf(api, url.QueryEscape(product), url.QueryEscape(store))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
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

	defer func() {
		if err = res.Body.Close(); err != nil {
			log.Error("resp body close failed")
		}
	}()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http code is %d: %w", res.StatusCode, ErrStatusCode)
	}

	return body, nil
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
	return productRegex.MatchString(product)
}

func isStore(store string) bool {
	return storeRegex.MatchString(store)
}

// watching watching Apple Store watcher.
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

// try remove targets.
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
