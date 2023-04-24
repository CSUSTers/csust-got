package base

import (
	"context"
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"fmt"
	gh "github.com/google/go-github/v35/github"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	. "gopkg.in/telebot.v3"
	"math/rand"
	"strings"
	"time"
)

// 生成一个6位的随机标识符
func generateIdentifier() string {
	const identifierChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var identifier strings.Builder
	source := rand.NewSource(time.Now().UnixNano())
	randomGen := rand.New(source)
	for i := 0; i < 6; i++ {
		identifier.WriteByte(identifierChars[randomGen.Intn(len(identifierChars))])
	}
	return identifier.String()
}

// 将URL添加到CSV文件并提交到GitHub仓库
func addURLToCSV(client *gh.Client, owner, repo, path, url string) (string, error) {
	ctx := context.Background()

	// 获取文件引用
	fileRef, _, _, err := client.Repositories.GetContents(ctx, owner, repo, path, &gh.RepositoryContentGetOptions{})
	if err != nil {
		return "", fmt.Errorf("couldn't get contents: %w", err)
	}

	// 获取现有的内容
	fileContent, err := fileRef.GetContent()
	if err != nil {
		return "", fmt.Errorf("couldn't get content: %w", err)
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

	// 生成一个新的标识符
	identifier := generateIdentifier()
	for identifiers[identifier] {
		identifier = generateIdentifier()
	}

	// 添加新的URL到CSV文件
	newContent := fmt.Sprintf("%s\n%s,%s", fileContent, identifier, url)

	// 更新文件
	message := fmt.Sprintf("Add URL: %s (short: s.csu.st/%s)", url, identifier)
	sha := fileRef.GetSHA()
	opts := &gh.RepositoryContentFileOptions{
		Message: &message,
		Content: []byte(newContent),
		SHA:     &sha,
	}
	_, _, err = client.Repositories.UpdateFile(ctx, owner, repo, path, opts)
	if err != nil {
		return "", fmt.Errorf("couldn't update file: %w", err)
	}

	return "https://s.csu.st/" + identifier + "\n", nil
}

// ShortUrlHandle handles the short url command.
func ShortUrlHandle(ctx Context) error {
	enabled := config.BotConfig.GithubConfig.Enabled
	if !enabled {
		err := ctx.Reply("未启用此功能，先去配置文件填写配置吧")
		if err != nil {
			log.Error("ShortUrlHandle: reply failed", zap.Error(err))
			return err
		}
	}
	// 提取命令参数中的url
	command := entities.FromMessage(ctx.Message())
	var args string
	if command.Argc() > 0 {
		args = command.ArgAllInOneFrom(0)
	}
	urls, err := findUrls(args)
	if err != nil {
		log.Error("ShortUrlHandle: findUrls failed", zap.Error(err))
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
	rplUrls := ""
	for _, urlStr := range urls {
		shortUrl, err := addURLToCSV(client, owner, repo, path, urlStr)
		if err != nil {
			log.Error("ShortUrlHandle: addURLToCSV failed", zap.Error(err))
			return err
		}
		rplUrls += shortUrl
	}
	err = ctx.Reply(rplUrls + "\n\n 以上是您的短链接，由于GitHub Action编译页面需要一定时间，请等待1-2分钟后再访问。")
	if err != nil {
		log.Error("ShortUrlHandle: reply failed", zap.Error(err))
	}
	return err
}
