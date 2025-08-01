package meili

import (
	"csust-got/config"
	"csust-got/log"
	"strconv"
	"sync"

	"go.uber.org/zap"

	"github.com/meilisearch/meilisearch-go"
)

type meiliData struct {
	Data   map[string]any
	ChatID int64
}

type searchQuery struct {
	Query         string
	IndexName     string
	SearchRequest meilisearch.SearchRequest
}

type searchResult struct {
	Result any
	Error  error
}

var (
	// dataChan pushes data to meili search.
	dataChan = make(chan meiliData, 100)
	// searchChan is used to pass search queries.
	searchChan = make(chan *searchQuery, 100)
	// resultChan is used to pass the search results back.
	resultChan = make(chan searchResult, 100)
	client     meilisearch.ServiceManager
	clientMux  sync.Mutex
	// once init meili at bot start.
	once sync.Once
	// filterOnceMap stores sync.Once for each index to ensure UpdateFilterableAttributes is called only once per index
	filterOnceMap = make(map[string]*sync.Once)
	filterOnceMux sync.Mutex
)

// InitMeili will start a meili worker goroutine
func InitMeili() {
	once.Do(func() {
		go StartWorker()
	})
}

func getClient() meilisearch.ServiceManager {
	clientMux.Lock()
	defer clientMux.Unlock()
	if client == nil {
		client = meilisearch.New(config.BotConfig.MeiliConfig.HostAddr, meilisearch.WithAPIKey(config.BotConfig.MeiliConfig.ApiKey))
	}
	return client
}

// getFilterOnce returns a sync.Once for the given index name, creating it if it doesn't exist
func getFilterOnce(indexName string) *sync.Once {
	filterOnceMux.Lock()
	defer filterOnceMux.Unlock()
	if filterOnceMap[indexName] == nil {
		filterOnceMap[indexName] = &sync.Once{}
	}
	return filterOnceMap[indexName]
}

// StartWorker will start meili worker
func StartWorker() {
	for {
		select {
		case data := <-dataChan:
			handleAddData(data)
		case query := <-searchChan:
			handleSearchQuery(query.Query, query.IndexName, &query.SearchRequest)
		}
	}
}

// handleAddData adds data to meili search.
func handleAddData(data meiliData) {
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
			return
		}
	}

	getFilterOnce(indexName).Do(func() {
		// Configure filterable attributes
		_, err := client.Index(indexName).UpdateFilterableAttributes(&[]string{"text", "caption"})
		if err != nil {
			log.Error("[MeiliSearch]: update filterable attributes failed", zap.Error(err), zap.String("index", indexName))
		} else {
			log.Debug("[MeiliSearch]: update filterable attributes success for index", zap.String("index", indexName))
		}
	})

	_, err = client.Index(indexName).AddDocuments(data.Data, "message_id")
	if err != nil {
		log.Error("[MeiliSearch]: add data to index failed", zap.Error(err))
		return
	}
	log.Debug("[MeiliSearch]: add data to index success", zap.Any("data", data.Data))
}

// handleSearchQuery searches the query and sends results back through the resultChan.
func handleSearchQuery(query string, indexName string, searchRequest *meilisearch.SearchRequest) {
	client := getClient()

	if searchRequest == nil {
		searchRequest = &meilisearch.SearchRequest{
			Limit: 10,
		}
	}

	searchResp, err := client.Index(indexName).Search(query, searchRequest)
	if err != nil {
		resultChan <- searchResult{Result: nil, Error: err}
		return
	}

	resultChan <- searchResult{Result: searchResp, Error: nil}
}

// AddData2Meili adds data to meili search.
func AddData2Meili(data map[string]any, chatID int64) {
	dataChan <- meiliData{Data: data, ChatID: chatID}
}

// SearchMeili performs a search query and returns results or error.
func SearchMeili(query *searchQuery) (any, error) {
	searchChan <- query
	result := <-resultChan
	return result.Result, result.Error
}
