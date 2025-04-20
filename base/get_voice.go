package base

import (
	"bufio"
	"cmp"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"math/rand/v2"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/meilisearch/meilisearch-go"
	"github.com/samber/lo"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// GetVoiceQuery is query of get voice
type GetVoiceQuery struct {
	Character string

	Text string
}

// VoiceChip is voice chip
type VoiceChip struct {
	Ch       string `json:"ch" mapstructure:"ch"`
	Text     string `json:"text" mapstructure:"text"`
	FullPath string `json:"full_path" mapstructure:"full_path"`
	File     string `json:"file" mapstructure:"file"`

	Url string
}

// GetVoiceResult is result of meilisearch
type GetVoiceResult struct {
	VoiceChip `mapstructure:",squash"`

	RankingScore float64 `json:"_rankingScore" mapstructure:"_rankingScore"`
}

// VoiceConfigNDb combines [`IndexConfig`] and [`VoiceDb`]
type VoiceConfigNDb struct {
	*config.IndexConfig

	*VoiceDb
}

var getVoiceAlias map[string]*VoiceConfigNDb
var getVoiceClient meilisearch.ServiceManager

// VoiceDb is get voice service database
type VoiceDb struct {
	ls []*VoiceChip

	index map[string][2]int
}

// NewVoiceDb returns a new [`VoiceDb`]
func NewVoiceDb(it iter.Seq[*VoiceChip]) *VoiceDb {
	db := VoiceDb{
		ls:    make([]*VoiceChip, 0, 1024),
		index: make(map[string][2]int),
	}

	db.ls = slices.SortedStableFunc(it, func(x, y *VoiceChip) int {
		return cmp.Compare(x.Ch, y.Ch)
	})
	db.ls = slices.Clip(db.ls)

	switch len(db.ls) {
	case 0:
	case 1:
		db.index[db.ls[0].Ch] = [...]int{0, 1}
	default:
		for i, v := range lo.Zip2(db.ls, db.ls[1:]) {
			x, y := v.Unpack()

			if idxT, ok := db.index[x.Ch]; ok {
				if y == nil || x.Ch != y.Ch {
					db.index[x.Ch] = [...]int{idxT[0], i + 1}
				}
			} else {
				db.index[x.Ch] = [...]int{i, i + 1}
			}
		}
	}
	// gob.NewEncoder(io.Discard).Encode(db)
	return &db
}

// RandomVoice returns a random voice
func (p *VoiceDb) RandomVoice() *VoiceChip {
	if len(p.ls) == 0 {
		return nil
	}
	return p.ls[rand.IntN(len(p.ls))]
}

// RandomVoiceByCh returns a random voice by character
func (p *VoiceDb) RandomVoiceByCh(ch string) *VoiceChip {
	if v, ok := p.index[ch]; ok {
		x, y := v[0], v[1]

		return p.ls[rand.N(y-x)+x]
	}
	return nil
}

// InitGetVoice init get voice service
func InitGetVoice() error {
	c := config.BotConfig.GetVoiceConfig

	if !c.Enable {
		return nil
	}

	getVoiceAlias = make(map[string]*VoiceConfigNDb)

	for _, index := range c.Indexes {
		n := &VoiceConfigNDb{
			IndexConfig: &index,
		}

		if index.Database != nil {
			t, file := index.Type, index.File

			fd, err := os.Open(file)
			if err != nil {
				log.Error("cannot open file", zap.String("file", file), zap.Error(err))
				return err
			}

			// 使用立即执行的匿名函数来处理文件，确保文件在处理完后关闭
			err = func() error {
				defer func() {
					if err := fd.Close(); err != nil {
						log.Error("failed to close file", zap.String("file", file), zap.Error(err))
					}
				}()

				var it iter.Seq[*VoiceChip]
				switch t {
				case "json":
					datas := []*VoiceChip{}
					de := json.NewDecoder(fd)
					err := de.Decode(&datas)
					if err != nil {
						log.Error("fail to decode json file", zap.String("file", file), zap.Error(err))
						return err
					}
					it = slices.Values(datas)
				case "jsonl", "ndjson":
					r := bufio.NewScanner(fd)
					it = func(yield func(*VoiceChip) bool) {
						for r.Scan() {
							if r.Err() != nil {
								log.Error("fail to read file", zap.String("file", file), zap.Error(r.Err()))
								return
							}

							s := r.Text()
							s = strings.TrimSpace(s)
							if s != "" {
								v := &VoiceChip{}
								err := json.Unmarshal([]byte(s), v)
								if err != nil {
									log.Error("fail to decode json line", zap.String("s", s), zap.Error(err))
									return
								}
								if !yield(v) {
									return
								}
							}
						}
					}
				default:
					// nolint: err113
					return fmt.Errorf("not support type: %s", t)
				}
				n.VoiceDb = NewVoiceDb(it)
				return nil
			}()

			if err != nil {
				return err
			}
		}

		for _, alias := range index.Alias {
			getVoiceAlias[alias] = n
		}
	}
	getVoiceClient = getMeilisearchClient(nil)

	return nil
}

func getMeilisearchClient(_ *config.GetVoiceConfig) meilisearch.ServiceManager {
	c := config.BotConfig.MeiliConfig
	opts := []meilisearch.Option{}
	if c.ApiKey != "" {
		opts = append(opts, meilisearch.WithAPIKey(c.ApiKey))
	}
	client := meilisearch.New(c.HostAddr, opts...)
	return client
}

var (
	// ErrIndexNotFound for index not found
	ErrIndexNotFound = errors.New("index not found")

	// ErrNoAudioFound for no audio found
	ErrNoAudioFound = errors.New("no audio found")
)

var idChars = append(lo.NumbersCharset, []rune("abcde")...)

func getVoiceMeta(indexName string, query *GetVoiceQuery) (ret *GetVoiceResult, err error) {
	index, ok := getVoiceAlias[indexName]
	if !ok {
		return nil, ErrIndexNotFound
	}
	idx := getVoiceClient.Index(index.IndexUid)

	random := query.Text == ""

	var filter = ""
	if query.Character != "" {
		filter = "ch = '" + query.Character + "'"
	}
	searchOpt := &meilisearch.SearchRequest{
		Filter:                  filter,
		ShowMatchesPosition:     true,
		ShowRankingScore:        true,
		ShowRankingScoreDetails: true,
	}

	// nolint:nestif
	if random {
		if index.VoiceDb != nil {
			if query.Character != "" {
				return &GetVoiceResult{VoiceChip: *index.RandomVoiceByCh(query.Character)}, nil
			}
			return &GetVoiceResult{VoiceChip: *index.RandomVoice()}, nil
		}

		ret = &GetVoiceResult{}

		filterTempl := "id STARTS WITH '%s'"
		if searchOpt.Filter != "" {
			filterTempl = fmt.Sprintf("(%s) AND (%s)", searchOpt.Filter, filterTempl)
		}

		var resp *meilisearch.SearchResponse
		for range 64 {
			// NEEDS TO ENABLE `containsFilter` EXPIRIMENTAL FEATURE
			// AND ADD `id` FIELD TO FILTER
			prefix := lo.RandomString(2, idChars)

			searchOpt.Filter = fmt.Sprintf(filterTempl, prefix)

			// TODO check meilisearch query
			searchOpt.Limit = 2000
			searchOpt.HitsPerPage = 1

			resp, err = idx.Search(query.Text, searchOpt)
			if err != nil {
				return nil, err
			}

			log.Debug("random get voice: checking",
				zap.Any("resp", resp))

			if resp.TotalHits > 0 {
				break
			}
		}

		if resp.TotalHits == 0 {
			searchOpt.Filter = filter
			resp, err = idx.Search(query.Text, searchOpt)
			if err != nil {
				return nil, err
			}
			if resp.TotalHits == 0 {
				return nil, ErrNoAudioFound
			}
		}

		searchOpt.HitsPerPage = 1
		searchOpt.Page = rand.N(resp.TotalHits)
		resp, err = idx.Search(query.Text, searchOpt)
		if err != nil {
			return nil, err
		}
		if len(resp.Hits) == 0 {
			return nil, ErrNoAudioFound
		}
		log.Debug("random get voice",
			zap.Any("resp", resp))

		v := &GetVoiceResult{}
		err = mapstructure.Decode(resp.Hits[0], v)
		if err != nil {
			return nil, err
		}
		ret = v
	} else {
		searchOpt.Limit = 2000
		searchOpt.HitsPerPage = 2000
		resp, err := idx.Search(query.Text, searchOpt)
		if err != nil {
			return nil, err
		}
		if len(resp.Hits) == 0 {
			return nil, ErrNoAudioFound
		}

		results := make([]*GetVoiceResult, 0, len(resp.Hits))
		err = mapstructure.Decode(resp.Hits, &results)
		if err != nil {
			return nil, err
		}
		res := lo.PartitionBy(results, func(v *GetVoiceResult) int {
			rank := int(math.Floor(v.RankingScore * 10000))
			switch {
			// case rank == 10000:
			// 	return 10000
			case rank >= 9999:
				return 9999
			// case rank >= 9990:
			// 	return 9990
			case rank >= 9900:
				return 9900
			case rank >= 9000:
				return 9000
			default:
				return 0
			}
		})
		if len(res[0]) > 0 {
			ret = res[0][rand.IntN(len(res[0]))]
		}
	}

	if ret != nil {
		ret.Url, err = url.JoinPath(index.VoiceBaseUrl, ret.FullPath)
	}

	return ret, err
}

func getVoice(ctx tb.Context, index string, text string) error {
	// 解析请求
	q := parseQueryText(text)

	meta, err := getVoiceMeta(index, q)
	if err != nil {
		return handleVoiceError(ctx, err, index)
	}

	// 获取游戏名称
	gameName := getGameNameFromIndex(index)

	// 格式化并发送消息
	caption := formatVoiceCaption(gameName, meta.Ch, meta.Text)
	return sendVoiceMessage(ctx, meta.Url, caption)
}

// GetVoice for get voice handle
func GetVoice(ctx tb.Context) error {
	if !config.BotConfig.GetVoiceConfig.Enable {
		return ctx.Send("功能未启用")
	}

	// 解析命令参数
	message := ctx.Message()

	cmd, rest, err := entities.CommandTakeArgs(message, 1)
	if err != nil {
		return ctx.Send("参数错误")
	}
	if cmd.Argc() == 0 {
		return getRandomVoiceFromAllGames(ctx)
	}

	if _, ok := getVoiceAlias[cmd.Arg(0)]; ok {
		return getVoice(ctx, cmd.Arg(0), rest)
	}

	_, rest, err = entities.CommandTakeArgs(message, 0)
	if err != nil {
		return ctx.Send("参数错误")
	}
	return searchVoiceFromAllGames(ctx, rest)
}

// searchVoiceFromAllGames 从所有游戏中搜索语音
func searchVoiceFromAllGames(ctx tb.Context, query string) error {
	if len(getVoiceAlias) == 0 {
		return ctx.Send("未配置任何语音库")
	}

	// 解析查询
	q := parseQueryText(query)

	// 从每个游戏中查询，找出最佳匹配
	var bestMatch *GetVoiceResult
	var bestMatchIndex string
	var bestScore float64

	for alias, indexConfig := range getVoiceAlias {
		// 跳过没有indexUid的配置
		if indexConfig.IndexUid == "" {
			continue
		}

		// 查询当前游戏
		result, err := getVoiceMeta(alias, q)
		if err == nil && result != nil && result.RankingScore > bestScore {
			bestMatch = result
			bestMatchIndex = alias
			bestScore = result.RankingScore
		}
	}

	// 处理查询结果
	if bestMatch != nil {
		gameName := getGameNameFromIndex(bestMatchIndex)
		caption := formatVoiceCaption(gameName, bestMatch.Ch, bestMatch.Text)
		return sendVoiceMessage(ctx, bestMatch.Url, caption)
	}

	return handleVoiceError(ctx, ErrNoAudioFound, "")
}

// formatVoiceCaption 格式化语音消息的 caption
func formatVoiceCaption(gameName, characterName, text string) string {
	// 格式化标签
	var gameTag string
	if gameName != "" {
		gameTag = "#" + strings.ReplaceAll(gameName, " ", "_")
	}
	characterTag := "#" + strings.ReplaceAll(characterName, " ", "_")

	// 封装文本
	blockquoteText := fmt.Sprintf("<blockquote expandable>%s</blockquote>", text)

	// 游戏名标签可能为空时的处理
	if gameTag != "" {
		return fmt.Sprintf("%s %s\n%s", gameTag, characterTag, blockquoteText)
	}
	return fmt.Sprintf("%s\n%s", characterTag, blockquoteText)
}

// getGameNameFromIndex 从索引配置中获取游戏名称
func getGameNameFromIndex(indexName string) string {
	indexConfig, ok := getVoiceAlias[indexName]
	if !ok || indexConfig.Name == "" {
		return "" // 当找不到索引或没有名称时返回空字符串
	}
	return indexConfig.Name
}

// sendVoiceMessage 发送语音消息
func sendVoiceMessage(ctx tb.Context, fileURL, caption string) error {
	return ctx.Send(&tb.Voice{
		File: tb.File{
			FileURL: fileURL,
		},
		Caption: caption,
	}, &tb.SendOptions{
		ParseMode: tb.ModeHTML,
	})
}

// handleVoiceError 处理错误响应
func handleVoiceError(ctx tb.Context, err error, indexName string) error {
	log.Error("fail to get voice meta", zap.String("index", indexName), zap.Error(err))

	// 获取游戏名称
	gameName := getGameNameFromIndex(indexName)

	// 根据错误类型构建错误信息
	var errorMsg string
	switch {
	case errors.Is(err, ErrIndexNotFound):
		errorMsg = "未找到语音索引"
	case errors.Is(err, ErrNoAudioFound):
		errorMsg = "未找到匹配的语音"
	default:
		errorMsg = "连接语音API服务器失败"
	}

	// 构建错误标签和caption
	var gameTag string
	if gameName != "" {
		gameTag = "#" + strings.ReplaceAll(gameName, " ", "_") + " "
	}
	errorCaption := fmt.Sprintf("%s%s\n<blockquote expandable>…异常…</blockquote>\n%s",
		gameTag, "#异常", errorMsg)

	// 发送错误响应
	errAudio := config.BotConfig.ErrAudioUrl
	if errAudio != "" {
		if sendErr := sendVoiceMessage(ctx, errAudio, errorCaption); sendErr != nil {
			log.Error("failed to send error audio", zap.Error(sendErr))
		}
	} else {
		if sendErr := ctx.Send(errorCaption, &tb.SendOptions{ParseMode: tb.ModeHTML}); sendErr != nil {
			log.Error("failed to send error message", zap.Error(sendErr))
		}
	}
	return err
}

// getRandomVoiceFromAllGames 从所有游戏中随机获取一条语音
func getRandomVoiceFromAllGames(ctx tb.Context) error {
	// 随机选择一个游戏别名
	if len(getVoiceAlias) == 0 {
		return ctx.Send("未配置任何语音库")
	}

	// 随机选择一个游戏别名
	randomGameAlias := lo.Keys(getVoiceAlias)[rand.IntN(len(getVoiceAlias))]

	// 从选中的游戏中获取随机语音
	return getVoice(ctx, randomGameAlias, "")
}

// parseQueryText 解析查询文本，提取角色和查询文本
func parseQueryText(text string) *GetVoiceQuery {
	q := &GetVoiceQuery{
		Text: text,
	}

	// 解析参数（如 ch=xx）
	patt := regexp.MustCompile(`(?i)^(?:\s*(?P<arg>\S+=(?:[^"'\s]\S*|\".*\"|\'*\'))\s*)`)
	cur := 0
	args := make([]string, 0)
	for {
		groups := patt.FindStringSubmatch(text[cur:])
		if len(groups) == 0 {
			break
		}
		args = append(args, groups[patt.SubexpIndex("arg")])
		cur += len(groups[0])
	}

	// 剩下的内容作为文本
	q.Text = text[cur:]

	// 处理参数
	for _, arg := range args {
		ss := strings.SplitN(arg, "=", 2)
		if len(ss) == 0 {
			continue
		}

		key := ss[0]
		value := ""
		if len(ss) >= 2 {
			value = strings.Trim(ss[1], `"'`)
		}

		switch key {
		case "ch", "角色":
			q.Character = value
		}
	}

	return q
}
