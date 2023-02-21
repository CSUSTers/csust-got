package sd

import (
	"bytes"
	"context"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/quic-go/quic-go/http3"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

var (
	mu       sync.Mutex
	ch       = make(chan *StableDiffusionContext, 10)
	busyUser = make(map[int64]int)
)

var httpClient *http.Client

type mixRoundTripper struct {
	TranditionalRoundTripper http.RoundTripper
	H3RoundTripper           http.RoundTripper
}

func newMixRoundTripper(t http.RoundTripper, h3 *http3.RoundTripper) *mixRoundTripper {
	return &mixRoundTripper{
		TranditionalRoundTripper: t,
		H3RoundTripper:           h3,
	}
}

func (r *mixRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.H3RoundTripper != nil {
		log.Debug("try h3", zap.String("url", util.ReplaceSpace(req.URL.String())))
		resp, err := r.H3RoundTripper.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
		log.Debug("h3 failed", zap.Error(err))
	}
	return r.TranditionalRoundTripper.RoundTrip(req)
}

func init() {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.IdleConnTimeout = 3 * time.Minute
	transport.ResponseHeaderTimeout = 3 * time.Minute

	dialer := net.Dialer{
		KeepAlive: 10 * time.Second,
	}
	transport.DialContext = dialer.DialContext

	h3RoundTripper := &http3.RoundTripper{
		QuicConfig: &quic.Config{
			MaxIdleTimeout:  3 * time.Minute,
			KeepAlivePeriod: 10 * time.Second,
		},
	}

	httpClient = &http.Client{
		Transport: newMixRoundTripper(transport, h3RoundTripper),
	}
}

// Handler stable diffusion handler.
func Handler(ctx Context) error {
	if !mu.TryLock() {
		return ctx.Reply("忙不过来了")
	}
	defer mu.Unlock()

	command := entities.FromMessage(ctx.Message())

	userID := ctx.Sender().ID
	config, err := getConfigByUserID(userID)
	if err != nil {
		return ctx.Reply("完了，删库跑路了")
	}

	if config.GetServer() == "" {
		return ctx.Reply("喂喂喂，你还没有配置服务器好吧。" +
			"快使用 /sdcfg 配置一个属于自己的服务器，或者找好心人捐赠一个服务器吧")
	}

	prompt := command.ArgAllInOneFrom(0)
	prompt = strings.ReplaceAll(prompt, "，", ",")
	if prompt == "" {
		prompt, _ = orm.GetSDLastPrompt(userID)
	} else {
		_ = orm.SetSDLastPrompt(userID, prompt)
	}

	req := config.GenStableDiffusionRequest()
	req.Prompt += ", " + prompt

	if busyUser[userID] >= 3 {
		return ctx.Reply("听我说你先别急，你还有3个没画完")
	}

	select {
	case ch <- &StableDiffusionContext{
		BotContext: ctx,
		UserConfig: *config,
		Request:    *req,
	}:
		busyUser[userID]++
		return ctx.Reply("在画了在画了")
	default:
		return ctx.Reply("忙不过来了")
	}

}

// Process is the stable diffusion background worker.
func Process() {
	lock := new(sync.Mutex)
	inUsedServer := make(map[string]chan *StableDiffusionContext)
	maxWorker := make(chan struct{}, 10)

	for ctx := range ch {
		select {
		case maxWorker <- struct{}{}:
			// Do nothing
		default:
			err := ctx.BotContext.Reply("任务堆积太多，忙不过来了。")
			if err != nil {
				log.Error("reply error", zap.Error(err))
			}
			continue
		}

		ctx := ctx
		server := ctx.UserConfig.GetServer()

		lock.Lock()
		serverCh, ok := inUsedServer[server]
		if !ok {
			serverCh = make(chan *StableDiffusionContext, 10)
			inUsedServer[ctx.UserConfig.GetServer()] = serverCh
		}
		lock.Unlock()
		serverCh <- ctx

		processFn := func() {
			for {
				select {
				case ctx := <-serverCh:
					func() {
						ctx := ctx
						defer func() {
							<-maxWorker

							mu.Lock()
							busyUser[ctx.BotContext.Sender().ID]--
							mu.Unlock()
						}()
						resp, err := requestStableDiffusion(ctx.UserConfig.GetServer(), &ctx.Request)
						if err != nil {
							err := ctx.BotContext.Reply("寄了")
							if err != nil {
								log.Error("reply stable diffusion failed", zap.Error(err))
							}
							return
						}

						photos := Album{}
						for _, v := range resp.Images {
							data, err := base64.StdEncoding.DecodeString(v)
							if err != nil {
								log.Error("decode stable diffusion image failed", zap.Error(err))
								continue
							}
							photos = append(photos, &Photo{
								File: File{FileReader: bytes.NewReader(data)},
							})
						}

						err = ctx.BotContext.SendAlbum(photos)
						if err != nil {
							log.Error("send stable diffusion album failed", zap.Error(err))
							err = ctx.BotContext.Reply("非常的寄")
							if err != nil {
								log.Error("reply stable diffusion failed", zap.Error(err))
							}
							return
						}
					}()
				default:
					lock.Lock()
					if len(serverCh) == 0 {
						delete(inUsedServer, server)
						lock.Unlock()
						return
					}
					lock.Unlock()
				}
			}
		}

		if !ok {
			go processFn()
		}
	}

}

/*
	{
	  "enable_hr": false,
	  "denoising_strength": 0,
	  "firstphase_width": 0,
	  "firstphase_height": 0,
	  "prompt": "",
	  "styles": [
	    "string"
	  ],
	  "seed": -1,
	  "subseed": -1,
	  "subseed_strength": 0,
	  "seed_resize_from_h": -1,
	  "seed_resize_from_w": -1,
	  "batch_size": 1,
	  "n_iter": 1,
	  "steps": 50,
	  "cfg_scale": 7,
	  "width": 512,
	  "height": 512,
	  "restore_faces": false,
	  "tiling": false,
	  "negative_prompt": "string",
	  "eta": 0,
	  "s_churn": 0,
	  "s_tmax": 0,
	  "s_tmin": 0,
	  "s_noise": 1,
	  "override_settings": {},
	  "sampler_index": "Euler"
	}
*/

// StableDiffusionReq is the request body of stable diffusion.
type StableDiffusionReq struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt"`
	Steps          int    `json:"steps"`
	CfgScale       int    `json:"cfg_scale"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	BatchSize      int    `json:"batch_size"`
	SamplerIndex   string `json:"sampler_index"`
}

/*
{
  "images": [
  ],
  "parameters": {
    "enable_hr": false,
    "denoising_strength": 0,
    "firstphase_width": 0,
    "firstphase_height": 0,
    "prompt": "girl",
    "styles": null,
    "seed": -1,
    "subseed": -1,
    "subseed_strength": 0,
    "seed_resize_from_h": -1,
    "seed_resize_from_w": -1,
    "batch_size": 1,
    "n_iter": 1,
    "steps": 50,
    "cfg_scale": 7,
    "width": 512,
    "height": 512,
    "restore_faces": false,
    "tiling": false,
    "negative_prompt": null,
    "eta": null,
    "s_churn": 0,
    "s_tmax": null,
    "s_tmin": 0,
    "s_noise": 1,
    "override_settings": null,
    "sampler_index": "Euler"
  },
  "info": {
    "prompt": "girl",
    "all_prompts": [
      "girl"
    ],
    "negative_prompt": "",
    "seed": 327883780,
    "all_seeds": [
      327883780
    ],
    "subseed": 887306102,
    "all_subseeds": [
      887306102
    ],
    "subseed_strength": 0,
    "width": 512,
    "height": 512,
    "sampler_index": 1,
    "sampler": "Euler",
    "cfg_scale": 7,
    "steps": 50,
    "batch_size": 1,
    "restore_faces": false,
    "face_restoration_model": null,
    "sd_model_hash": "e6e8e1fc",
    "seed_resize_from_w": -1,
    "seed_resize_from_h": -1,
    "denoising_strength": 0,
    "extra_generation_params": {},
    "index_of_first_image": 0,
    "infotexts": [
      "girl\nSteps: 50, Sampler: Euler, CFG scale: 7.0, Seed: 327883780, Size: 512x512, Model hash: e6e8e1fc,
	  Seed resize from: -1x-1, Denoising strength: 0, Clip skip: 2"
    ],
    "styles": [],
    "job_timestamp": "0",
    "clip_skip": 2
  }
}
*/

// StableDiffusionResp is the response of stable diffusion
type StableDiffusionResp struct {
	Images []string `json:"images"`
}

func requestStableDiffusion(addr string, req *StableDiffusionReq) (*StableDiffusionResp, error) {
	if addr == "" {
		return nil, ErrServerNotConfigured
	}

	bs, err := json.Marshal(req)
	if err != nil {
		log.Error("marshal stable diffusion request failed", zap.Error(err))
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	httpReq, err := http.NewRequest("POST", addr, bytes.NewReader(bs))
	if err != nil {
		log.Error("create stable diffusion request failed", zap.Error(err))
		return nil, err
	}
	httpReq = httpReq.WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	// httpReq.Header.Set("Expect", "100-continue")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Error("request stable diffusion failed", zap.Error(err))
		return nil, fmt.Errorf("request stable diffusion failed: %w", ErrServerNotAvailable)
	}
	defer func() { _ = resp.Body.Close() }()

	bts, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("read stable diffusion response body failed", zap.Error(err))
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Error("stable diffusion response status code is not 200",
			zap.Int("status code", resp.StatusCode), zap.String("response body", string(bts)))
		return nil, fmt.Errorf("%w: request stable diffusion failed, status code: %d, response: %s",
			ErrRequestNotOK, resp.StatusCode, string(bts))
	}

	var respData StableDiffusionResp
	err = json.Unmarshal(bts, &respData)
	if err != nil {
		log.Error("unmarshal stable diffusion response failed", zap.Error(err))
		return nil, err
	}

	return &respData, nil
}

// nolint: unused // for some reason
func joinApi(baseUrl, path string) string {
	if baseUrl == "" {
		return ""
	}
	baseUrl = strings.TrimSuffix(baseUrl, "/")
	return baseUrl + path
}
