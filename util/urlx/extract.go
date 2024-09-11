package urlx

import (
	"bytes"
	"csust-got/log"
	"net/url"
	"strings"
)

// ExtraType for [`Extra`]
type ExtraType int

const (
	// TypePlain for text
	TypePlain ExtraType = iota

	// TypeUrl for url
	TypeUrl
)

// Extra extracts text to [`Extra`]
type Extra struct {
	Type ExtraType

	// Raw text
	Text string

	// Url
	Url *ExtraUrl
}

// ExtraUrl is extracted url
type ExtraUrl struct {
	// Full url string
	// example: `https://example.com/echo?q=hello#hash`
	Text string

	// Scheme
	// example: `https``
	Scheme string

	// Domain
	// example: `example.com``
	Domain string

	// Tld
	// example: `com``
	Tld string

	// Port
	// example: <empty>
	Port string

	// Path
	// example: `/echo``
	Path string

	// Query
	// example: `?q=hello`
	Query string

	// Hash
	// example: `#world`
	Hash string
}

// UrlToExtraUrl convert `*url.Url` to `*ExtraUrl`
func UrlToExtraUrl(u *url.URL) *ExtraUrl {
	scheme := strings.TrimSuffix(u.Scheme, ":")
	schemeStr := ""
	if scheme != "" {
		schemeStr = scheme + "://"
	}

	host := u.Hostname()
	tld := ""
	subHosts := strings.Split(host, ".")
	if len(subHosts) > 1 {
		tld = subHosts[len(subHosts)-1]
	}

	port := u.Port()
	portStr := ""
	if port != "" {
		portStr = ":" + port
	}
	path := u.Path
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	query := u.Query().Encode()
	if query != "" {
		query = "?" + query
	}
	hash := u.Fragment
	if hash != "" {
		hash = "#" + hash
	}

	urlStr := schemeStr + host + portStr + path + query + hash
	return &ExtraUrl{
		Text:   urlStr,
		Scheme: scheme,
		Domain: host,
		Tld:    tld,
		Port:   port,
		Path:   path,
		Query:  query,
		Hash:   hash,
	}
}

// StringByFields return a URL from Extracted Fields
func (u *ExtraUrl) StringByFields() string {
	buf := bytes.NewBufferString("")

	// scheme: `https://`
	if u.Scheme != "" {
		buf.WriteString(u.Scheme)
		buf.WriteString("://")
	}

	// domain: `example.com`
	buf.WriteString(u.Domain)

	// port: `:8080`
	if u.Port != "" {
		buf.WriteString(":")
		buf.WriteString(u.Port)
	}

	// path: `/echo`
	buf.WriteString(u.Path)

	// query: `?foo=bar`
	buf.WriteString(u.Query)

	// hash: `#hash`
	buf.WriteString(u.Hash)

	return buf.String()
}

// ExtractStr extracts text to [`Extra`] list
func ExtractStr(text string) (extras []*Extra) {
	if len(text) == 0 {
		return []*Extra{}
	}

	cur := 0
	extras = make([]*Extra, 0)
	for _, m := range Patt.FindAllStringSubmatchIndex(text, -1) {
		urlGroupIdx := Patt.SubexpIndex("url")
		if urlGroupIdx < 0 {
			log.Fatal("Url regex must have a group named `url`")
		}
		begin := m[urlGroupIdx*2]
		end := m[urlGroupIdx*2+1]

		if begin > cur {
			extras = append(extras, &Extra{
				Type: TypePlain,
				Text: text[cur:begin],
			})
		}
		cur = end

		extras = append(extras, &Extra{
			Type: TypeUrl,
			Text: text[begin:end],
			Url: &ExtraUrl{
				Text:   text[begin:end],
				Scheme: SubmatchGroupStringByName(Patt, text, m, "scheme"),
				Domain: SubmatchGroupStringByName(Patt, text, m, "domain"),
				Tld:    SubmatchGroupStringByName(Patt, text, m, "tld"),
				Port:   SubmatchGroupStringByName(Patt, text, m, "port"),
				Path:   SubmatchGroupStringByName(Patt, text, m, "path"),
				Query:  SubmatchGroupStringByName(Patt, text, m, "query"),
				Hash:   SubmatchGroupStringByName(Patt, text, m, "hash"),
			},
		})
	}
	if cur < len(text) {
		extras = append(extras, &Extra{
			Type: TypePlain,
			Text: text[cur:],
		})
	}
	return extras
}
