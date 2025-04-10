package sd

import (
	"csust-got/entities"
	"csust-got/orm"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
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

	HiResEnabled         string  `json:"hr"`
	DenoisingStrength    float64 `json:"denoising_strength"`
	HiResScale           float64 `json:"hr_scale"`
	HiResUpscaler        string  `json:"hr_upscaler"`
	HiResSecondPassSteps int     `json:"hr_second_pass_steps"`
}

// GetValueByKey get value by key.
func (c *StableDiffusionConfig) GetValueByKey(key string) interface{} {
	switch key {
	case "server":
		return "🤫"
	case "prompt":
		if c.Prompt == "" {
			return "masterpiece, best quality"
		}
		return c.Prompt
	case "negative_prompt":
		if c.NegativePrompt == "" {
			return "nsfw, lowres, bad anatomy, bad hands, (((deformed))), [blurry], (poorly drawn hands), (poorly drawn feet), " +
				"text, error, missing fingers, extra digit, " +
				"fewer digits, cropped, worst quality, low quality, normal quality, jpeg artifacts, signature, watermark, username, blurry"
		}
		return c.NegativePrompt
	case "steps":
		if c.Steps == 0 {
			return 28
		}
		return c.Steps
	case "scale":
		if c.Scale == 0 {
			return 7
		}
		return c.Scale
	case "width":
		if c.Width == 0 {
			return 512
		}
		return c.Width
	case "height":
		if c.Height == 0 {
			return 512
		}
		return c.Height
	case "res":
		return fmt.Sprintf("%dx%d", c.GetValueByKey("width"), c.GetValueByKey("height"))
	case "number":
		if c.Number == 0 {
			return 1
		}
		return c.Number
	case "sampler":
		if c.Sampler == "" {
			return "Euler a"
		}
		return c.Sampler
	case "hr":
		if c.HiResEnabled == "" {
			return "off"
		}
		return c.HiResEnabled
	case "denoising_strength":
		if c.DenoisingStrength == 0 {
			return 0.6
		}
		return c.DenoisingStrength
	case "hr_scale":
		if c.HiResScale == 0 {
			return 2.0
		}
		return c.HiResScale
	case "hr_upscaler":
		if c.HiResUpscaler == "" {
			return "Latent"
		}
		return c.HiResUpscaler
	case "hr_second_pass_steps":
		if c.HiResSecondPassSteps == 0 {
			return 20
		}
		return c.HiResSecondPassSteps
	default:
		return "key not exists"
	}
}

// SetValueByKey set config value by key.
func (c *StableDiffusionConfig) SetValueByKey(key string, value string) error {
	switch key {
	case "server":
		if value == "*" {
			c.Server = ""
		} else {
			c.Server = strings.TrimSuffix(value, "/")
		}
	case "prompt":
		c.Prompt = value
	case "negative_prompt":
		c.NegativePrompt = value
	case "steps":
		steps, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: steps must be a integer", ErrConfigIsInvalid)
		}
		c.Steps = steps
		if c.Steps < 1 || c.Steps > 50 {
			return fmt.Errorf("%w: steps too small or too large", ErrConfigIsInvalid)
		}
	case "scale":
		scale, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: scale must be a integer", ErrConfigIsInvalid)
		}
		c.Scale = scale
		if c.Scale < 1 || c.Scale > 20 {
			return fmt.Errorf("%w: scale too small or too large", ErrConfigIsInvalid)
		}
	case "width":
		width, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: width must be a integer", ErrConfigIsInvalid)
		}
		c.Width = width / 64 * 64
		if c.Width < 1 || c.Width > 1024 {
			return fmt.Errorf("%w: width too small or too large", ErrConfigIsInvalid)
		}
	case "height":
		height, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: height must be a integer", ErrConfigIsInvalid)
		}
		c.Height = height / 64 * 64
		if c.Height < 1 || c.Height > 1024 {
			return fmt.Errorf("%w: height too small or too large", ErrConfigIsInvalid)
		}
	case "res":
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
	case "number":
		number, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: number must be a integer", ErrConfigIsInvalid)
		}
		c.Number = number
		if c.Number < 1 || c.Number > 4 {
			return fmt.Errorf("%w: number too small or too large", ErrConfigIsInvalid)
		}
	case "sampler":
		if value == "*" {
			value = "Euler a"
		}
		c.Sampler = value
	case "hr":
		if value == "on" {
			c.HiResEnabled = "on"
		} else {
			c.HiResEnabled = "off"
		}
	case "denoising_strength":
		denoisingStrength, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%w: denoising_strength must be a float", ErrConfigIsInvalid)
		}
		c.DenoisingStrength = denoisingStrength
		if c.DenoisingStrength < 0 || c.DenoisingStrength > 1 {
			return fmt.Errorf("%w: denoising_strength must be between 0 and 1", ErrConfigIsInvalid)
		}
	case "hr_scale":
		hrScale, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%w: hr_scale must be a float", ErrConfigIsInvalid)
		}
		c.HiResScale = hrScale
		if c.HiResScale < 1 || c.HiResScale > 4 {
			return fmt.Errorf("%w: hr_scale must be between 1 and 4", ErrConfigIsInvalid)
		}
	case "hr_upscaler":
		if value == "*" {
			value = "Latent"
		}
		c.HiResUpscaler = value
	case "hr_second_pass_steps":
		hrSecondPassSteps, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: hr_second_pass_steps must be a integer", ErrConfigIsInvalid)
		}
		c.HiResSecondPassSteps = hrSecondPassSteps
		if c.HiResSecondPassSteps < 0 || c.HiResSecondPassSteps > 50 {
			return fmt.Errorf("%w: hr_second_pass_steps too small or too large", ErrConfigIsInvalid)
		}
	default:
		return fmt.Errorf("%w: invalid key: %s", ErrConfigIsInvalid, key)
	}
	return nil
}

// GetServer return server.
func (c *StableDiffusionConfig) GetServer() string {
	server := c.Server
	if server == "" {
		server = orm.GetSDDefaultServer()
	}
	server = strings.TrimSuffix(server, "/")
	return server
}

// GenStableDiffusionRequest generate stable diffusion request by config.
func (c *StableDiffusionConfig) GenStableDiffusionRequest() *StableDiffusionReq {
	req := &StableDiffusionReq{
		Prompt:         c.GetValueByKey("prompt").(string),
		NegativePrompt: c.GetValueByKey("negative_prompt").(string),
		Steps:          c.GetValueByKey("steps").(int),
		CfgScale:       c.GetValueByKey("scale").(int),
		Width:          c.GetValueByKey("width").(int),
		Height:         c.GetValueByKey("height").(int),
		BatchSize:      c.GetValueByKey("number").(int),
		SamplerIndex:   c.GetValueByKey("sampler").(string),
	}
	if c.GetValueByKey("hr").(string) == "on" {
		req.HiResEnabled = true
		req.DenoisingStrength = c.GetValueByKey("denoising_strength").(float64)
		req.HiResScale = c.GetValueByKey("hr_scale").(float64)
		req.HiResUpscaler = c.GetValueByKey("hr_upscaler").(string)
		req.HiResSecondPassSteps = c.GetValueByKey("hr_second_pass_steps").(int)
		req.BatchSize = 1
	}
	return req
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
	"`sampler`: sampler for stable diffusion, default is `Euler a`\\.\n" +
	"`hr`: high resolution fix `on`/`off`, will force `number` to 1\\.\n" +
	"`denoising_strength`: denoising strength for high resolution\\.\n" +
	"`hr_scale`: high resolution scale\\.\n" +
	"`hr_upscaler`: high resolution upscaler, default is `Latent`\\.\n" +
	"`hr_second_pass_steps`: high resolution fix steps\\."

const (
	sdSubCmdSet = "set"
	sdSubCmdGet = "get"
)

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

	var mode, key, value string
	switch command.Arg(0) {
	case sdSubCmdSet:
		if command.Argc() < 3 {
			return ctx.Reply(helpInfo, ModeMarkdownV2)
		}
		mode = sdSubCmdSet
		key = command.Arg(1)
		value = command.ArgAllInOneFrom(2)
	case sdSubCmdGet:
		if command.Argc() < 2 {
			return ctx.Reply(helpInfo, ModeMarkdownV2)
		}
		mode = sdSubCmdGet
		key = command.Arg(1)
	default:
		if command.Argc() == 1 {
			mode = sdSubCmdGet
			key = command.Arg(0)
		} else {
			mode = sdSubCmdSet
			key = command.Arg(0)
			value = command.ArgAllInOneFrom(1)
		}
	}

	switch mode {
	case sdSubCmdSet:
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
	case sdSubCmdGet:
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
