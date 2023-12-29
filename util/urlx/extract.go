package urlx

import "csust-got/log"

type ExtraType int

const (
	Plain ExtraType = iota
	Url
)

type Extra struct {
	Type ExtraType

	// Raw text
	Text string

	// Url
	Url *ExtraUrl
}

type ExtraUrl struct {
	// Full url string
	// example: `https://example.com/echo?q=hello#hash`
	Text string

	// Schema
	// example: `https``
	Schema string

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

func ExtractStr(text string) (extras []*Extra) {
	if len(text) == 0 {
		return
	}

	cur := 0
	for _, m := range Patt.FindAllStringSubmatchIndex(text, -1) {
		urlGroupIdx := Patt.SubexpIndex("url")
		if urlGroupIdx < 0 {
			log.Fatal("Url regex must have a group named `url`")
		}
		begin := m[urlGroupIdx*2]
		end := m[urlGroupIdx*2+1]

		if begin > cur {
			extras = append(extras, &Extra{
				Type: Plain,
				Text: text[cur:begin],
			})
		}
		cur = end

		extras = append(extras, &Extra{
			Type: Url,
			Text: text[begin:end],
			Url: &ExtraUrl{
				Text:   text[begin:end],
				Schema: SubmatchGroupStringByName(Patt, text, m, "schema"),
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
			Type: Plain,
			Text: text[cur:],
		})
	}
	return
}
