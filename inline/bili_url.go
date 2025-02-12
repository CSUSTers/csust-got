package inline

import (
	"bytes"
	"context"
	"csust-got/log"
	"csust-got/util/urlx"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

const b23URL = "b23.tv"

var urlPathPatt = regexp.MustCompile(`(?i)(?:/)(?P<fragment>[^/\s]*)`)
var biliVideoIdPatt = regexp.MustCompile(`(?i)^((?:av|ep)(?:\d+)|bv(?:[a-zA-Z0-9]+))$`)

var startWithHttpScheme = regexp.MustCompile(`(?i)^[0-9a-z\-]+://.*`)

var biliDomains = []string{
	b23URL,
	"bilibili.com",
	"www.bilibili.com",
	"space.bilibili.com",
	"m.bilibili.com",
	"t.bilibili.com",
	"live.bilibili.com",
}

var biliRetainQueryParams = []string{
	"p",
	"t",
	"tab",
	"start_progress",
}

var (
	bProcessor = newBiliProcessor(biliDomains, biliRetainQueryParams, defaultProcessConfig)
)

func init() {
	registerUrlProcessor(bProcessor)
}

// biliProcessor b站 URL 处理器
type biliProcessor struct {
	domainMap    map[string]struct{}
	retainParams []string

	processConfig
}

func newBiliProcessor(domain []string, retainParams []string, c processConfig) urlProcessor {
	proc := &biliProcessor{
		domainMap:     make(map[string]struct{}),
		retainParams:  retainParams,
		processConfig: c,
	}
	for _, d := range domain {
		proc.domainMap[d] = struct{}{}
	}
	return proc
}

func (c *biliProcessor) needProcess(u *urlx.Extra) bool {
	d := strings.ToLower(u.Url.Domain)
	_, ok := c.domainMap[d]
	return ok
}

func (c *biliProcessor) clearBiliUrlQuery(u *urlx.ExtraUrl) error {
	q, err := filterParamFromQuery(u.Query, c.retainParams...)
	if err != nil {
		return err
	}
	u.Query = q
	return nil
}

func (c *biliProcessor) writeUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.processConfig.Timeout)
	defer cancel()

	if strings.ToLower(u.Domain) == b23URL {
		to, err := c.processB23Url(ctx, u)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				log.Error("bili url request timeout", zap.Error(err), zap.String("url", u.Text))
			}
			return err
		}
		buf.WriteString(to)
	} else {
		err := c.clearBiliUrlQuery(u)
		if err != nil {
			return err
		}
		buf.WriteString(u.StringByFields())
	}
	return nil
}

func (c *biliProcessor) processB23Url(ctx context.Context, u *urlx.ExtraUrl) (string, error) {
	path := u.Path
	pathFragm := splitUrlPath(path)
	if len(pathFragm) == 0 {
		if u.Query == "" {
			return u.Text, nil
		}
		err := c.clearBiliUrlQuery(u)
		if err != nil {
			return "", err
		}
		return u.StringByFields(), nil
	}

	// process origin video URL
	firstFr := pathFragm[0]
	if biliVideoIdPatt.MatchString(firstFr) {
		u.Path = "/" + firstFr
		err := c.clearBiliUrlQuery(u)
		if err != nil {
			return "", err
		}
		return u.StringByFields(), nil
	}

	// process short video URL
	return c.processBiliShortenUrl(ctx, u)
}

func (c *biliProcessor) processBiliShortenUrl(ctx context.Context, u *urlx.ExtraUrl) (string, error) {
	oriUrl := u.Text
	oriUrl = fixUrl(oriUrl)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oriUrl, nil)
	if err != nil {
		return "", err
	}

	client := http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.ToLower(req.URL.Hostname()) != b23URL {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	// get origin URL from a shorten URL
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		to, err := resp.Location()
		if err != nil {
			return "", err
		}
		e := urlx.UrlToExtraUrl(to)

		if strings.HasPrefix(e.Path, "/video/") {
			q := to.Query()
			e.Query = getBiliVideoUrlQuery(q)

			paths := splitUrlPath(e.Path)
			if len(paths) >= 2 && e.Query == "" {
				e.Path = "/" + paths[1]
				e.Domain = "b23.tv"
			}
		}
		err = c.clearBiliUrlQuery(e)
		if err != nil {
			return "", err
		}
		return e.StringByFields(), nil
	}

	return u.Text, nil
}

func splitUrlPath(path string) []string {
	matches := urlPathPatt.FindAllStringSubmatchIndex(path, -1)

	ret := make([]string, 0, len(matches))
	for _, m := range matches {
		ret = append(ret, urlx.SubmatchGroupStringByName(urlPathPatt, path, m, "fragment"))
	}
	return ret
}

func fixUrl(s string) string {
	if !startWithHttpScheme.MatchString(s) {
		return "http://" + s
	}
	return s
}

// video URL without `p` and `t` query params
// use `b23.tv` domain for shorten URL
//
// P.S. sometimes `start_progress` is used for marking video time
//
//	e.g. `start_progress=71527` may mean `t=71.5`
//	replace it with `t` query param in current version
func getBiliVideoUrlQuery(q url.Values) string {

	pQ := q.Get("p")
	tQ := q.Get("t")
	stpQ := q.Get("start_progress")
	if stpQ != "" {
		ts, err := strconv.ParseFloat(stpQ, 32)
		if err == nil {
			tQ = strconv.FormatFloat(ts/1000, 'f', 1, 32)
		} else {
			log.Error("convert video `start_progress` failed", zap.String("string", stpQ), zap.Error(err))
		}
	}

	clear(q)
	if pQ != "" && pQ != "1" {
		q.Set("p", pQ)
	}
	if tQ != "" {
		q.Set("t", tQ)
	}

	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
