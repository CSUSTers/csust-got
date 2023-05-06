package base

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"fmt"
	gh "github.com/google/go-github/v35/github"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	. "gopkg.in/telebot.v3"
	"strings"
	"time"
)

// 将多个URL添加到CSV文件并提交到GitHub仓库
func addURLsToCSV(client *gh.Client, owner, repo, path string, urls []string) ([]string, error) {
	ctx := context.Background()

	// 获取文件引用
	fileRef, _, _, err := client.Repositories.GetContents(ctx, owner, repo, path, &gh.RepositoryContentGetOptions{})
	if err != nil {
		return nil, fmt.Errorf("couldn't get contents: %w", err)
	}

	// 获取现有的内容
	fileContent, err := fileRef.GetContent()
	if err != nil {
		return nil, fmt.Errorf("couldn't get content: %w", err)
	}
	// 去掉最后一个换行符
	fileContent = strings.TrimSuffix(fileContent, "\n")
	existingLines := strings.Split(fileContent, "\n")
	identifiers := map[string]bool{}
	for _, line := range existingLines[1:] {
		parts := strings.SplitN(line, ",", 2)
		if len(parts) > 0 {
			identifiers[strings.TrimSpace(parts[0])] = true
		}
	}

	// 生成多个新的标识符
	identifierGen := util.NewRandStrWithLength(6)
	newURLs := make([]string, 0, len(urls))
	newLines := make([]string, 0, len(urls))

	for _, url := range urls {
		identifier := identifierGen.RandStr()
		for identifiers[identifier] {
			identifier = identifierGen.RandStr()
		}
		newURL := "https://" + config.BotConfig.GithubConfig.ShortUrlPrefix + "/" + identifier
		newURLs = append(newURLs, newURL)
		newLines = append(newLines, fmt.Sprintf("%s,%s", identifier, url))
	}

	newContent := fmt.Sprintf("%s\n%s", fileContent, strings.Join(newLines, "\n"))

	// 更新文件
	message := fmt.Sprintf("Add URLs: %s", urls)
	sha := fileRef.GetSHA()
	opts := &gh.RepositoryContentFileOptions{
		Message: &message,
		Content: []byte(newContent),
		SHA:     &sha,
	}
	_, _, err = client.Repositories.UpdateFile(ctx, owner, repo, path, opts)
	if err != nil {
		return nil, fmt.Errorf("couldn't update file: %w", err)
	}

	return newURLs, nil
}

// ShortUrlHandle handles the short url command.
func ShortUrlHandle(ctx Context) error {
	enabled := config.BotConfig.GithubConfig.Enabled
	if !enabled {
		err := ctx.Reply("未启用此功能，先去配置文件填写配置吧")
		if err != nil {
			log.Error("[slink]: ShortUrlHandle: reply failed", zap.Error(err))
			return err
		}
	}

	// 提取命令参数中的url
	args := ctx.Message().Text
	if ctx.Message().ReplyTo != nil {
		args += ctx.Message().ReplyTo.Text
	}

	urls, err := findUrls(args)
	if err != nil {
		log.Error("[slink]: ShortUrlHandle: findUrls failed", zap.Error(err))
		return err
	}
	if len(urls) == 0 {
		return ctx.Reply("亲爱的朋友，当寂静的夜晚降临，繁星闪烁，我们寻找着那消逝的信标。在这无尽的网络宇宙里，我们似飘渺的船只，需要您的统一资源定位符作为明灯，照亮迷茫的道路。")
	}
	ghCtx := context.Background()

	accessToken := config.BotConfig.GithubConfig.Token
	owner := config.BotConfig.GithubConfig.Owner
	repo := config.BotConfig.GithubConfig.Repo
	path := config.BotConfig.GithubConfig.Path

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: accessToken},
	)
	tc := oauth2.NewClient(ghCtx, ts)

	client := gh.NewClient(tc)
	rplUrls, err := addURLsToCSV(client, owner, repo, path, urls)
	if err != nil {
		log.Error("[slink]: ShortUrlHandle: addURLToCSV failed", zap.Error(err))
		return err
	}

	msg, err := util.SendReplyWithError(ctx.Chat(), strings.Join(rplUrls, " ⌛\n\n")+
		" ⌛\n\n \n以上是您的短链接，请等待1-2分钟，待就绪指示器变绿后再访问。", ctx.Message())
	if err != nil {
		log.Error("[slink]: ShortUrlHandle: reply failed", zap.Error(err))
	}
	go updateUrlStatus(msg, rplUrls)
	return err
}

// updateUrlStatus pooling the urls status, and update the message when the url is ready.
func updateUrlStatus(msg *Message, urls []string) {
	startTime := time.Now()
	log.Debug("[slink]: ", zap.Strings("urls", urls))
	for len(urls) > 0 {
		for _, oneUrl := range urls {
			log.Debug("[slink]: checking …… ", zap.String("oneUrl", oneUrl))
			if util.CheckUrl(oneUrl) {
				text := strings.Replace(msg.Text, oneUrl+" ⌛", oneUrl+" ✅", 1)
				log.Debug("[slink]: reply changed with success ", zap.String("text", text))
				newMsg, err := util.EditMessageWithError(msg, text)
				if err != nil {
					log.Error("[slink]: updateUrlStatus: edit message failed", zap.Error(err))
					return
				}
				msg = newMsg
				urls = util.DeleteSlice(urls, oneUrl)
			}
			if time.Since(startTime) > 5*time.Minute {
				text := strings.Replace(msg.Text, oneUrl+" ⌛", oneUrl+" ❌", 1)
				log.Debug("[slink]: reply changed with falling ", zap.String("text", text))
				newMsg, err := util.EditMessageWithError(msg, text)
				if err != nil {
					log.Error("[slink]: updateUrlStatus: edit message failed", zap.Error(err))
					return
				}
				msg = newMsg
				urls = util.DeleteSlice(urls, oneUrl)
			}
			time.Sleep(5 * time.Second)
		}
	}
}
