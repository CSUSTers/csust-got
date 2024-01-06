package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"net/url"
	"regexp"
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

type needProcessFunc func(u *urlx.Extra) bool

type writeUrlFunc func(buf *bytes.Buffer, u *urlx.ExtraUrl) error

type urlProcessConfig struct {
	needProcess needProcessFunc
	handler     writeUrlFunc
}

var urlProcessConfigs []*urlProcessConfig

func registerRegexUrlProcessConfig(regex string, handler writeUrlFunc) {
	re := regexp.MustCompile(regex)
	urlProcessConfigs = append(urlProcessConfigs, &urlProcessConfig{
		needProcess: func(u *urlx.Extra) bool {
			return re.MatchString(u.Url.Domain)
		},
		handler: handler,
	})
}

func registerDomainsUrlProcessConfig(domains []string, handler writeUrlFunc) {
	domainMap := make(map[string]bool)
	for _, d := range domains {
		domainMap[d] = true
	}
	urlProcessConfigs = append(urlProcessConfigs, &urlProcessConfig{
		needProcess: func(u *urlx.Extra) bool {
			return domainMap[u.Url.Domain]
		},
		handler: handler,
	})
}
