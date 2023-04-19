package meili

import (
	"csust-got/config"
	"csust-got/log"
	"go.uber.org/zap"
	"strconv"
	"sync"

	"github.com/meilisearch/meilisearch-go"
)

type meiliData struct {
	Data   map[string]interface{}
	ChatID int64
}

var (
	dataChan  = make(chan meiliData, 100)
	client    *meilisearch.Client
	clientMux sync.Mutex
)

func getClient() *meilisearch.Client {
	clientMux.Lock()
	defer clientMux.Unlock()
	if client == nil {
		client = meilisearch.NewClient(meilisearch.ClientConfig{
			Host:   config.BotConfig.MeiliConfig.HostAddr,
			APIKey: config.BotConfig.MeiliConfig.ApiKey,
		})
	}
	return client
}

func init() {
	go func() {
		for data := range dataChan {
			client := getClient()
			indexName := config.BotConfig.MeiliConfig.IndexPrefix + strconv.FormatInt(data.ChatID, 10)
			_, err := client.Index(indexName).FetchInfo()

			if err != nil {
				indexCfg := &meilisearch.IndexConfig{
					Uid:        indexName,
					PrimaryKey: "message_id",
				}
				_, err = client.CreateIndex(indexCfg)
				if err != nil {
					log.Error("[MeiliSearch]: create index failed", zap.Error(err))
					continue
				}
			}

			_, err = client.Index(indexName).AddDocuments(data.Data, "message_id")
			if err != nil {
				log.Error("[MeiliSearch]: create index failed", zap.Error(err))
				continue
			}
			log.Debug("[MeiliSearch]: add data to index success", zap.Any("data", data.Data))
		}
	}()
}

// AddData2Meili add data to meili search.
func AddData2Meili(data map[string]interface{}, chatID int64) {
	dataChan <- meiliData{Data: data, ChatID: chatID}
}
