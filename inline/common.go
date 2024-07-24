package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"net/url"
	"time"
)

func filterParamFromQuery(query string, keepParams ...string) (string, error) {
	if query == "" {
		return "", nil
	}

	if query[0] == '?' {
		query = query[1:]
	}

	old, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}

	newMap := make(url.Values)
	for _, k := range keepParams {
		if v, ok := old[k]; ok {
			newMap[k] = v
		}
	}

	ret := newMap.Encode()
	if ret != "" {
		ret = "?" + ret
	}
	return ret, nil
}

type urlProcessor interface {
	needProcess(u *urlx.Extra) bool
	writeUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error
}

var urlProcessConfigs []urlProcessor

func registerUrlProcessor(processor ...urlProcessor) {
	urlProcessConfigs = append(urlProcessConfigs, processor...)
}

type processConfig struct {
	Timeout time.Duration
}

var defaultProcessConfig = processConfig{
	Timeout: 10 * time.Second,
}
