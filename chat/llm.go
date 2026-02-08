package chat

import (
	"csust-got/config"
	"csust-got/log"
	"net/http"
	"net/url"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
	"go.uber.org/zap"
)

var lcModels map[string]llms.Model

// InitLangchainModels initializes langchaingo LLM models for each unique model config.
func InitLangchainModels(configs []*config.ChatConfigSingle) {
	lcModels = make(map[string]llms.Model)

	for _, c := range configs {
		if !c.Agent.Enabled {
			continue
		}
		if _, ok := lcModels[c.Model.Name]; ok {
			continue
		}

		opts := []openai.Option{
			openai.WithToken(c.Model.ApiKey),
			openai.WithModel(c.Model.Model),
		}

		if c.Model.BaseUrl != "" {
			opts = append(opts, openai.WithBaseURL(c.Model.BaseUrl))
		}

		if c.Model.Proxy != "" {
			proxyURL, err := url.Parse(c.Model.Proxy)
			if err != nil {
				log.Fatal("failed to parse proxy URL for langchain model", zap.Error(err))
			}
			httpClient := &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(proxyURL),
				},
			}
			opts = append(opts, openai.WithHTTPClient(httpClient))
		}

		llm, err := openai.New(opts...)
		if err != nil {
			log.Fatal("failed to create langchain model",
				zap.String("name", c.Model.Name), zap.Error(err))
		}

		lcModels[c.Model.Name] = llm
		log.Info("initialized langchain model", zap.String("name", c.Model.Name))
	}
}

// getLangchainModel returns the langchaingo model for the given model name.
func getLangchainModel(name string) llms.Model {
	if lcModels == nil {
		return nil
	}
	return lcModels[name]
}
