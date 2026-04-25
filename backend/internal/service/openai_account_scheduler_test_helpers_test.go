package service

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"golang.org/x/sync/singleflight"
)

type schedulerTestOpenAIAccountRepo = stubOpenAIAccountRepo
type schedulerTestGatewayCache = stubGatewayCache
type schedulerTestConcurrencyCache = stubConcurrencyCache

type schedulerTestSettingRepo struct {
	value string
}

func (r schedulerTestSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, errors.New("not implemented")
}

func (r schedulerTestSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != openAIAdvancedSchedulerSettingKey {
		return "", errors.New("not found")
	}
	return r.value, nil
}

func (r schedulerTestSettingRepo) Set(context.Context, string, string) error {
	return errors.New("not implemented")
}

func (r schedulerTestSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("not implemented")
}

func (r schedulerTestSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return errors.New("not implemented")
}

func (r schedulerTestSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("not implemented")
}

func (r schedulerTestSettingRepo) Delete(context.Context, string) error {
	return errors.New("not implemented")
}

func newOpenAIAdvancedSchedulerRateLimitService(value string) *RateLimitService {
	return &RateLimitService{
		cfg: &config.Config{},
		settingService: &SettingService{
			settingRepo: schedulerTestSettingRepo{value: value},
		},
	}
}

func newSchedulerTestOpenAIWSV2Config() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	return cfg
}

func resetOpenAIAdvancedSchedulerSettingCacheForTest() {
	openAIAdvancedSchedulerSettingCache.Store((*cachedOpenAIAdvancedSchedulerSetting)(nil))
	openAIAdvancedSchedulerSettingSF = singleflight.Group{}
}
