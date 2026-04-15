package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// creditsExhaustedKey 是 model_rate_limits 中标记积分耗尽的特殊 key。
	// 与普通模型限流完全同构：通过 SetModelRateLimit / isRateLimitActiveForKey 读写。
	creditsExhaustedKey      = "AICredits"
	creditsExhaustedDuration = 5 * time.Hour
	googleOneAICreditType    = "GOOGLE_ONE_AI"
	creditsBalanceCheckTTL   = 8 * time.Second
)

type antigravity429Category string

const (
	antigravity429Unknown        antigravity429Category = "unknown"
	antigravity429RateLimited    antigravity429Category = "rate_limited"
	antigravity429QuotaExhausted antigravity429Category = "quota_exhausted"
)

var (
	antigravityQuotaExhaustedKeywords = []string{
		"quota_exhausted",
		"quota exhausted",
	}

	creditsExhaustedKeywords = []string{
		"google_one_ai",
		"insufficient credit",
		"insufficient credits",
		"not enough credit",
		"not enough credits",
		"credit exhausted",
		"credits exhausted",
		"credit balance",
		"minimumcreditamountforusage",
		"minimum credit amount for usage",
	}

	loadCodeAssistForCreditsCheck = func(ctx context.Context, proxyURL, accessToken string) (*antigravity.LoadCodeAssistResponse, error) {
		client, err := antigravity.NewClient(proxyURL)
		if err != nil {
			return nil, err
		}
		loadResp, _, err := client.LoadCodeAssist(ctx, accessToken)
		if err != nil {
			return nil, err
		}
		return loadResp, nil
	}
)

// isCreditsExhausted 检查账号的 AICredits 限流 key 是否生效（积分是否耗尽）。
func (a *Account) isCreditsExhausted() bool {
	if a == nil {
		return false
	}
	return a.isRateLimitActiveForKey(creditsExhaustedKey)
}

// setCreditsExhausted 标记账号积分耗尽：写入 model_rate_limits["AICredits"] + 更新缓存。
func (s *AntigravityGatewayService) setCreditsExhausted(ctx context.Context, account *Account) {
	if account == nil || account.ID == 0 {
		return
	}
	resetAt := time.Now().Add(creditsExhaustedDuration)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, creditsExhaustedKey, resetAt); err != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "set credits exhausted failed: account=%d err=%v", account.ID, err)
		return
	}
	s.updateAccountModelRateLimitInCache(ctx, account, creditsExhaustedKey, resetAt)
	logger.LegacyPrintf("service.antigravity_gateway", "credits_exhausted_marked account=%d reset_at=%s",
		account.ID, resetAt.UTC().Format(time.RFC3339))
}

// clearCreditsExhausted 清除账号的 AICredits 限流 key。
func (s *AntigravityGatewayService) clearCreditsExhausted(ctx context.Context, account *Account) {
	if err := clearCreditsExhaustedState(ctx, s.accountRepo, account); err != nil {
		accountID := int64(0)
		if account != nil {
			accountID = account.ID
		}
		logger.LegacyPrintf("service.antigravity_gateway", "clear credits exhausted failed: account=%d err=%v", accountID, err)
	}
}

func clearCreditsExhaustedState(ctx context.Context, repo AccountRepository, account *Account) error {
	if account == nil || account.ID == 0 || account.Extra == nil {
		return nil
	}
	rawLimits, ok := account.Extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		return nil
	}
	if _, exists := rawLimits[creditsExhaustedKey]; !exists {
		return nil
	}
	delete(rawLimits, creditsExhaustedKey)
	account.Extra[modelRateLimitsKey] = rawLimits
	if repo == nil {
		return nil
	}
	if err := repo.UpdateExtra(ctx, account.ID, map[string]any{
		modelRateLimitsKey: rawLimits,
	}); err != nil {
		return err
	}
	return nil
}

// classifyAntigravity429 将 Antigravity 的 429 响应归类为配额耗尽、限流或未知。
func classifyAntigravity429(body []byte) antigravity429Category {
	if len(body) == 0 {
		return antigravity429Unknown
	}
	lowerBody := strings.ToLower(string(body))
	for _, keyword := range antigravityQuotaExhaustedKeywords {
		if strings.Contains(lowerBody, keyword) {
			return antigravity429QuotaExhausted
		}
	}
	if info := parseAntigravitySmartRetryInfo(body); info != nil && !info.IsModelCapacityExhausted {
		return antigravity429RateLimited
	}
	return antigravity429Unknown
}

// injectEnabledCreditTypes 在已序列化的 v1internal JSON body 中注入 AI Credits 类型。
func injectEnabledCreditTypes(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	payload["enabledCreditTypes"] = []string{"GOOGLE_ONE_AI"}
	result, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return result
}

// resolveCreditsOveragesModelKey 解析当前请求对应的 overages 状态模型 key。
func resolveCreditsOveragesModelKey(ctx context.Context, account *Account, upstreamModelName, requestedModel string) string {
	modelKey := strings.TrimSpace(upstreamModelName)
	if modelKey != "" {
		return modelKey
	}
	if account == nil {
		return ""
	}
	modelKey = resolveFinalAntigravityModelKey(ctx, account, requestedModel)
	if strings.TrimSpace(modelKey) != "" {
		return modelKey
	}
	return resolveAntigravityModelKey(requestedModel)
}

// shouldMarkCreditsExhausted 判断一次 credits 请求失败是否应标记为 credits 耗尽。
func shouldMarkCreditsExhausted(resp *http.Response, respBody []byte, reqErr error) bool {
	if reqErr != nil || resp == nil {
		return false
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
		return false
	}
	if info := parseAntigravitySmartRetryInfo(respBody); info != nil {
		return false
	}
	bodyLower := strings.ToLower(string(respBody))
	for _, keyword := range creditsExhaustedKeywords {
		if strings.Contains(bodyLower, keyword) {
			return true
		}
	}
	return false
}

func extractAICreditsFromLoadCodeAssist(loadResp *antigravity.LoadCodeAssistResponse) []AICredit {
	if loadResp == nil {
		return nil
	}
	availableCredits := loadResp.GetAvailableCredits()
	if len(availableCredits) == 0 {
		return nil
	}
	credits := make([]AICredit, 0, len(availableCredits))
	for _, credit := range availableCredits {
		credits = append(credits, AICredit{
			CreditType:     credit.CreditType,
			Amount:         credit.GetAmount(),
			MinimumBalance: credit.GetMinimumAmount(),
		})
	}
	return credits
}

func findGoogleOneAICredit(credits []AICredit) (AICredit, bool) {
	for _, credit := range credits {
		if strings.EqualFold(strings.TrimSpace(credit.CreditType), googleOneAICreditType) {
			return credit, true
		}
	}
	return AICredit{}, false
}

func hasUsableGoogleOneAICredits(usage *UsageInfo) bool {
	if usage == nil {
		return false
	}
	credit, ok := findGoogleOneAICredit(usage.AICredits)
	return ok && credit.Amount > credit.MinimumBalance
}

func syncCreditsExhaustedStateFromUsage(ctx context.Context, repo AccountRepository, account *Account, usage *UsageInfo) (bool, error) {
	if !hasUsableGoogleOneAICredits(usage) {
		return false, nil
	}
	if err := clearCreditsExhaustedState(ctx, repo, account); err != nil {
		return false, err
	}
	return true, nil
}

func checkCreditsBalance(ctx context.Context, proxyURL, accessToken string) (AICredit, bool, error) {
	if strings.TrimSpace(accessToken) == "" {
		return AICredit{}, false, errors.New("missing access token")
	}
	checkCtx, cancel := context.WithTimeout(ctx, creditsBalanceCheckTTL)
	defer cancel()

	loadResp, err := loadCodeAssistForCreditsCheck(checkCtx, proxyURL, accessToken)
	if err != nil {
		return AICredit{}, false, err
	}
	credit, found := findGoogleOneAICredit(extractAICreditsFromLoadCodeAssist(loadResp))
	return credit, found, nil
}

type creditsOveragesRetryResult struct {
	handled bool
	resp    *http.Response
}

// attemptCreditsOveragesRetry 在确认免费配额耗尽后，尝试注入 AI Credits 继续请求。
func (s *AntigravityGatewayService) attemptCreditsOveragesRetry(
	p antigravityRetryLoopParams,
	baseURL string,
	modelName string,
	waitDuration time.Duration,
	originalStatusCode int,
	respBody []byte,
) *creditsOveragesRetryResult {
	creditsBody := injectEnabledCreditTypes(p.body)
	if creditsBody == nil {
		return &creditsOveragesRetryResult{handled: false}
	}
	modelKey := resolveCreditsOveragesModelKey(p.ctx, p.account, modelName, p.requestedModel)
	logger.LegacyPrintf("service.antigravity_gateway", "%s status=429 credit_overages_retry model=%s account=%d (injecting enabledCreditTypes)",
		p.prefix, modelKey, p.account.ID)

	creditsReq, err := antigravity.NewAPIRequestWithURL(p.ctx, baseURL, p.action, p.accessToken, creditsBody)
	if err != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d build_request_err=%v",
			p.prefix, modelKey, p.account.ID, err)
		return &creditsOveragesRetryResult{handled: true}
	}

	creditsResp, err := p.httpUpstream.Do(creditsReq, p.proxyURL, p.account.ID, p.account.Concurrency)
	if err == nil && creditsResp != nil && creditsResp.StatusCode < 400 {
		s.clearCreditsExhausted(p.ctx, p.account)
		logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d credit_overages_success model=%s account=%d",
			p.prefix, creditsResp.StatusCode, modelKey, p.account.ID)
		return &creditsOveragesRetryResult{handled: true, resp: creditsResp}
	}

	s.handleCreditsRetryFailure(p.ctx, p.prefix, modelKey, p.account, p.proxyURL, p.accessToken, creditsResp, err)
	return &creditsOveragesRetryResult{handled: true}
}

func (s *AntigravityGatewayService) handleCreditsRetryFailure(
	ctx context.Context,
	prefix string,
	modelKey string,
	account *Account,
	proxyURL string,
	accessToken string,
	creditsResp *http.Response,
	reqErr error,
) {
	var creditsRespBody []byte
	creditsStatusCode := 0
	if creditsResp != nil {
		creditsStatusCode = creditsResp.StatusCode
		if creditsResp.Body != nil {
			creditsRespBody, _ = io.ReadAll(io.LimitReader(creditsResp.Body, 64<<10))
			_ = creditsResp.Body.Close()
		}
	}

	if shouldMarkCreditsExhausted(creditsResp, creditsRespBody, reqErr) && account != nil {
		creditBalance, hasCreditsBalance, balanceErr := checkCreditsBalance(ctx, proxyURL, accessToken)
		if balanceErr != nil {
			logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d marked_exhausted=false status=%d confirm_balance_err=%v body=%s",
				prefix, modelKey, account.ID, creditsStatusCode, balanceErr, truncateForLog(creditsRespBody, 200))
			return
		}
		if hasCreditsBalance && creditBalance.Amount > creditBalance.MinimumBalance {
			if err := clearCreditsExhaustedState(ctx, s.accountRepo, account); err != nil {
				logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d marked_exhausted=false status=%d confirm_balance_clear_err=%v amount=%.4f minimum=%.4f body=%s",
					prefix, modelKey, account.ID, creditsStatusCode, err, creditBalance.Amount, creditBalance.MinimumBalance, truncateForLog(creditsRespBody, 200))
				return
			}
			logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d marked_exhausted=false status=%d confirmed_credits_available=true amount=%.4f minimum=%.4f body=%s",
				prefix, modelKey, account.ID, creditsStatusCode, creditBalance.Amount, creditBalance.MinimumBalance, truncateForLog(creditsRespBody, 200))
			return
		}
		s.setCreditsExhausted(ctx, account)
		logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d marked_exhausted=true status=%d credits_found=%t amount=%.4f minimum=%.4f body=%s",
			prefix, modelKey, account.ID, creditsStatusCode, hasCreditsBalance, creditBalance.Amount, creditBalance.MinimumBalance, truncateForLog(creditsRespBody, 200))
		return
	}
	if account != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d marked_exhausted=false status=%d err=%v body=%s",
			prefix, modelKey, account.ID, creditsStatusCode, reqErr, truncateForLog(creditsRespBody, 200))
	}
}
