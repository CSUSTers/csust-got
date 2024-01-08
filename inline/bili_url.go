package inline

import (
	"bytes"
	"context"
	"csust-got/util/urlx"
	"net/http"
	"regexp"
	"strings"
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
}

var (
	bProcessor = newBiliProcessor(biliDomains, biliRetainQueryParams)
)

func init() {
	registerUrlProcessor(bProcessor)
}

// biliProcessor b站 URL 处理器
type biliProcessor struct {
	domainMap    map[string]struct{}
	retainParams []string
}

func newBiliProcessor(domain []string, retainParams []string) urlProcessor {
	proc := &biliProcessor{
		domainMap:    make(map[string]struct{}),
		retainParams: retainParams,
	}
	for _, d := range domain {
		proc.domainMap[d] = struct{}{}
	}
	return proc
}

func (c *biliProcessor) needProcess(u *urlx.Extra) bool {
	_, ok := c.domainMap[u.Url.Domain]
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
	if strings.ToLower(u.Domain) == b23URL {
		to, err := c.processB23Url(context.TODO(), u)
		if err != nil {
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

		// video URL without `p` and `t` query params
		// use `b23.tv` domain for shorten URL
		if strings.HasPrefix(e.Path, "/video/") {
			pQ := to.Query().Get("p")
			tQ := to.Query().Get("t")
			paths := splitUrlPath(e.Path)
			if len(paths) >= 2 && (pQ == "" || pQ == "1") && tQ == "" {
				e.Path = "/" + paths[1]
				e.Domain = "b23.tv"
				e.Query = ""
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
