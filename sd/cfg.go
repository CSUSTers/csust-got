package sd

import (
	"csust-got/entities"
	"csust-got/orm"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-redis/redis/v7"
	. "gopkg.in/telebot.v3"
)

// StableDiffusionConfig is the config of stable diffusion.
type StableDiffusionConfig struct {
	Server         string `json:"server"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt"`
	Steps          int    `json:"steps"`
	Scale          int    `json:"scale"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Number         int    `json:"number"`
	Sampler        string `json:"sampler"`
}

// GetValueByKey get value by key.
func (c *StableDiffusionConfig) GetValueByKey(key string) interface{} {
	switch {
	case key == "server":
		return "🤫"
	case key == "prompt":
		if c.Prompt == "" {
			return "masterpiece, best quality"
		}
		return c.Prompt
	case key == "negative_prompt":
		if c.NegativePrompt == "" {
			return "nsfw, lowres, bad anatomy, bad hands, text, error, missing fingers, extra digit, " +
				"fewer digits, cropped, worst quality, low quality, normal quality, jpeg artifacts, signature, watermark, username, blurry"
		}
		return c.NegativePrompt
	case key == "steps":
		if c.Steps == 0 {
			return 28
		}
		return c.Steps
	case key == "scale":
		if c.Scale == 0 {
			return 7
		}
		return c.Scale
	case key == "width":
		if c.Width == 0 {
			return 512
		}
		return c.Width
	case key == "height":
		if c.Height == 0 {
			return 512
		}
		return c.Height
	case key == "res":
		return fmt.Sprintf("%dx%d", c.GetValueByKey("width"), c.GetValueByKey("height"))
	case key == "number":
		if c.Number == 0 {
			return 1
		}
		return c.Number
	case key == "sampler":
		if c.Sampler == "" {
			return "Euler a"
		}
		return c.Sampler
	default:
		return "key not exists"
	}
}

// SetValueByKey set config value by key.
func (c *StableDiffusionConfig) SetValueByKey(key string, value string) error {
	switch {
	case key == "server":
		if value == "*" {
			c.Server = ""
		} else {
			server := strings.TrimSuffix(value, "/")
			c.Server = server
		}
	case key == "prompt":
		c.Prompt = value
	case key == "negative_prompt":
		c.NegativePrompt = value
	case key == "steps":
		steps, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: steps must be a integer", ErrConfigIsInvalid)
		}
		c.Steps = steps
		if c.Steps < 1 || c.Steps > 50 {
			return fmt.Errorf("%w: steps too small or too large", ErrConfigIsInvalid)
		}
	case key == "scale":
		scale, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: scale must be a integer", ErrConfigIsInvalid)
		}
		c.Scale = scale
		if c.Scale < 1 || c.Scale > 20 {
			return fmt.Errorf("%w: scale too small or too large", ErrConfigIsInvalid)
		}
	case key == "width":
		width, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: width must be a integer", ErrConfigIsInvalid)
		}
		c.Width = width / 64 * 64
		if c.Width < 1 || c.Width > 1024 {
			return fmt.Errorf("%w: width too small or too large", ErrConfigIsInvalid)
		}
	case key == "height":
		height, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: height must be a integer", ErrConfigIsInvalid)
		}
		c.Height = height / 64 * 64
		if c.Height < 1 || c.Height > 1024 {
			return fmt.Errorf("%w: height too small or too large", ErrConfigIsInvalid)
		}
	case key == "res":
		res := strings.Split(value, "x")
		if len(res) != 2 {
			return fmt.Errorf("%w: invalid resolution", ErrConfigIsInvalid)
		}
		if err := c.SetValueByKey("width", res[0]); err != nil {
			return err
		}
		if err := c.SetValueByKey("height", res[1]); err != nil {
			return err
		}
	case key == "number":
		number, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: number must be a integer", ErrConfigIsInvalid)
		}
		c.Number = number
		if c.Number < 1 || c.Number > 4 {
			return fmt.Errorf("%w: number too small or too large", ErrConfigIsInvalid)
		}
	case key == "sampler":
		if value == "*" {
			value = "Euler a"
		}
		c.Sampler = value
	default:
		return fmt.Errorf("%w: invalid key: %s", ErrConfigIsInvalid, key)
	}
	return nil
}

// GetServer return server.
func (c *StableDiffusionConfig) GetServer() string {
	server := c.Server
	if server == "" {
		return orm.GetSDDefaultServer()
	}
	return server
}

// GenStableDiffusionRequest generate stable diffusion request by config.
func (c *StableDiffusionConfig) GenStableDiffusionRequest() *StableDiffusionReq {
	return &StableDiffusionReq{
		Prompt:         c.GetValueByKey("prompt").(string),
		NegativePrompt: c.GetValueByKey("negative_prompt").(string),
		Steps:          c.GetValueByKey("steps").(int),
		CfgScale:       c.GetValueByKey("scale").(int),
		Width:          c.GetValueByKey("width").(int),
		Height:         c.GetValueByKey("height").(int),
		BatchSize:      c.GetValueByKey("number").(int),
		SamplerIndex:   c.GetValueByKey("sampler").(string),
	}
}

const helpInfo = "sdcfg set \\<key\\> \\<value\\>\n" +
	"sdcfg get \\<key\\>\n" +
	"available keys: \n" +
	"`server`: your own stable diffusion server address\\(write only\\)\\.\n" +
	"`prompt`: your default prompt, will add to your every command call\\.\n" +
	"`negative_prompt`: your default negative prompt, will add to your every command call\\.\n" +
	"`steps`: steps for stable diffusion\\.\n" +
	"`scale`: scale for stable diffusion\\.\n" +
	"`res`: resolution __width__x__height__\\.\n" +
	"`number`: number of images for once command call\\.\n" +
	"`sampler`: sampler for stable diffusion\\."

// ConfigHandler handle /sdcfg command.
func ConfigHandler(ctx Context) error {
	command := entities.FromMessage(ctx.Message())

	if command.Argc() == 0 {
		return ctx.Reply(helpInfo, ModeMarkdownV2)
	}

	userID := ctx.Sender().ID
	config, err := getConfigByUserID(userID)
	if err != nil {
		return ctx.Reply("完了，删库跑路了")
	}

	switch command.Arg(0) {
	case "set":
		if command.Argc() < 3 {
			return ctx.Reply(helpInfo, ModeMarkdownV2)
		}
		key := command.Arg(1)
		value := command.ArgAllInOneFrom(2)
		err = config.SetValueByKey(key, value)
		if err != nil {
			return ctx.Reply(err.Error())
		}
		configStr, err := json.MarshalIndent(&config, "", "")
		if err != nil {
			return ctx.Reply("感觉有点问题")
		}
		err = orm.SetSDConfig(userID, string(configStr))
		if err != nil {
			return ctx.Reply("完了，删库跑路了")
		}
		return ctx.Reply("配置保存成功")
	case "get":
		if command.Argc() < 2 {
			return ctx.Reply(helpInfo, ModeMarkdownV2)
		}
		key := command.Arg(1)
		return ctx.Reply(fmt.Sprintf("`%v`", config.GetValueByKey(key)), ModeMarkdownV2)
	}

	return ctx.Reply(helpInfo, ModeMarkdownV2)
}

func getConfigByUserID(userID int64) (*StableDiffusionConfig, error) {
	config := &StableDiffusionConfig{}
	configStr, err := orm.GetSDConfig(userID)
	if err != nil && !errors.Is(err, redis.Nil) {
		return config, err
	}
	if err == nil {
		err = json.Unmarshal([]byte(configStr), &config)
		if err != nil {
			return config, err
		}
	}
	return config, nil
}
