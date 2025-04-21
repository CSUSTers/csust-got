package prom

import (
	"net/http"

	// _ "net/http/pprof" // pprof

	"csust-got/config"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// InitPrometheus init prometheus.
func InitPrometheus() {
	cfg := config.BotConfig
	if cfg.PromConfig.Enabled {
		http.Handle("/metrics", promhttp.Handler())
	}

	go func() {
		err := http.ListenAndServe(cfg.Listen, nil)
		if err != nil {
			zap.L().Error("InitPrometheus: Serve http failed", zap.Error(err))
		}
	}()
}
