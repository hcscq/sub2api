package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// creditsExhaustedKey 是 model_rate_limits 中标记积分耗尽的特殊 key。
	// 与普通模型限流完全同构：通过 SetModelRateLimit / isRateLimitActiveForKey 读写。
	creditsExhaustedKey                = "AICredits"
	creditsRequestInsufficientKey      = "AICreditsRequestInsufficient"
	antigravityCreditsOveragesStateKey = "antigravity_credits_overages"
	creditsExhaustedDuration           = 5 * time.Hour
	creditsRequestInsufficientCooldown = 15 * time.Minute
	creditsOveragesMinimumCooldown     = 15 * time.Minute
	creditsOveragesDefaultCooldown     = 5 * time.Hour
	googleOneAICreditType              = "GOOGLE_ONE_AI"
	creditsBalanceCheckTTL             = 8 * time.Second
	creditsAmountField                 = "confirmed_credit_amount"
	creditsMinimumBalanceField         = "minimum_balance"
	creditsReasonField                 = "reason"
	creditsRequestInsufficientReason   = "request_insufficient"
	creditsActiveUntilField            = "active_until"
	creditsActivatedAtField            = "activated_at"
	creditsLastSuccessAtField          = "last_success_at"
	creditsOveragesActiveReason        = "quota_exhausted"
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

func buildAntigravityCreditsOveragesExtraKey(modelKey string) string {
	normalized := normalizeAntigravityModelName(modelKey)
	if normalized == "" {
		return ""
	}
	return antigravityCreditsOveragesStateKey + ":" + normalized
}

func isAntigravityCreditsOveragesRuntimeKey(key string) bool {
	key = strings.TrimSpace(key)
	return key == antigravityCreditsOveragesStateKey || strings.HasPrefix(key, antigravityCreditsOveragesStateKey+":")
}

func clearAntigravityCreditsOveragesRuntimeState(extra map[string]any) {
	if extra == nil {
		return
	}
	for key := range extra {
		if isAntigravityCreditsOveragesRuntimeKey(key) {
			delete(extra, key)
		}
	}
}

func parseExtraTime(v any) (time.Time, bool) {
	switch value := v.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
			return parsed, true
		}
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
			return parsed, true
		}
	case time.Time:
		return value, true
	}
	return time.Time{}, false
}

func (a *Account) antigravityCreditsOveragesActiveUntilByModelKey(modelKey string) *time.Time {
	if a == nil || a.Platform != PlatformAntigravity || a.Extra == nil {
		return nil
	}
	extraKey := buildAntigravityCreditsOveragesExtraKey(modelKey)
	if extraKey == "" {
		return nil
	}
	rawState, ok := a.Extra[extraKey].(map[string]any)
	if !ok {
		return nil
	}
	activeUntil, ok := parseExtraTime(rawState[creditsActiveUntilField])
	if !ok {
		return nil
	}
	return &activeUntil
}

func (a *Account) getAntigravityCreditsOveragesRemainingByModelKey(modelKey string) time.Duration {
	activeUntil := a.antigravityCreditsOveragesActiveUntilByModelKey(modelKey)
	if activeUntil == nil {
		return 0
	}
	if remaining := time.Until(*activeUntil); remaining > 0 {
		return remaining
	}
	return 0
}

func (a *Account) isAntigravityCreditsOveragesActiveWithContext(ctx context.Context, requestedModel string) bool {
	if a == nil || a.Platform != PlatformAntigravity {
		return false
	}
	modelKey := resolveCreditsOveragesModelKey(ctx, a, "", requestedModel)
	return a.getAntigravityCreditsOveragesRemainingByModelKey(modelKey) > 0
}

func (a *Account) requiresAntigravityCreditsForModelWithContext(ctx context.Context, requestedModel string) bool {
	if a == nil || a.Platform != PlatformAntigravity {
		return false
	}
	if a.isModelRateLimitedWithContext(ctx, requestedModel) {
		return true
	}
	return a.isAntigravityCreditsOveragesActiveWithContext(ctx, requestedModel)
}

func resolveCreditsOveragesActiveDuration(waitDuration time.Duration) time.Duration {
	if override, ok := antigravityFallbackCooldownSeconds(); ok {
		return override
	}
	if waitDuration >= creditsOveragesMinimumCooldown {
		return waitDuration
	}
	if waitDuration > 0 {
		return creditsOveragesMinimumCooldown
	}
	return creditsOveragesDefaultCooldown
}

// isCreditsExhausted 检查账号的 AICredits 限流 key 是否生效（积分是否耗尽）。
func (a *Account) isCreditsExhausted() bool {
	if a == nil {
		return false
	}
	return a.isRateLimitActiveForKey(creditsExhaustedKey) ||
		a.isRateLimitActiveForKey(creditsRequestInsufficientKey)
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
	if err := clearCreditsSelectionState(ctx, s.accountRepo, account); err != nil {
		accountID := int64(0)
		if account != nil {
			accountID = account.ID
		}
		logger.LegacyPrintf("service.antigravity_gateway", "clear credits exhausted failed: account=%d err=%v", accountID, err)
	}
}

func clearCreditsSelectionState(ctx context.Context, repo AccountRepository, account *Account) error {
	_, err := clearCreditsRateLimitKeys(ctx, repo, account, creditsExhaustedKey, creditsRequestInsufficientKey)
	return err
}

func clearCreditsRateLimitKeys(ctx context.Context, repo AccountRepository, account *Account, keys ...string) (bool, error) {
	if account == nil || account.ID == 0 || account.Extra == nil {
		return false, nil
	}
	rawLimits, ok := account.Extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		return false, nil
	}
	deleted := false
	for _, key := range keys {
		if _, exists := rawLimits[key]; exists {
			delete(rawLimits, key)
			deleted = true
		}
	}
	if !deleted {
		return false, nil
	}
	account.Extra[modelRateLimitsKey] = rawLimits
	if repo == nil {
		return true, nil
	}
	if err := repo.UpdateExtra(ctx, account.ID, map[string]any{
		modelRateLimitsKey: rawLimits,
	}); err != nil {
		return false, err
	}
	return true, nil
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

func getCreditsRequestInsufficientAmount(account *Account) (float64, bool) {
	if account == nil || account.Extra == nil {
		return 0, false
	}
	rawLimits, ok := account.Extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		return 0, false
	}
	rawLimit, ok := rawLimits[creditsRequestInsufficientKey].(map[string]any)
	if !ok {
		return 0, false
	}
	return parseExtraFloat(rawLimit[creditsAmountField])
}

func parseExtraFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func shouldClearCreditsRequestInsufficientFromUsage(account *Account, usage *UsageInfo) bool {
	if !hasUsableGoogleOneAICredits(usage) {
		return false
	}
	current, ok := findGoogleOneAICredit(usage.AICredits)
	if !ok {
		return false
	}
	lastFailedAmount, ok := getCreditsRequestInsufficientAmount(account)
	if !ok {
		return false
	}
	return current.Amount > lastFailedAmount
}

func syncCreditsExhaustedStateFromUsage(ctx context.Context, repo AccountRepository, account *Account, usage *UsageInfo) (bool, error) {
	if !hasUsableGoogleOneAICredits(usage) {
		return false, nil
	}
	keys := []string{creditsExhaustedKey}
	if shouldClearCreditsRequestInsufficientFromUsage(account, usage) {
		keys = append(keys, creditsRequestInsufficientKey)
	}
	cleared, err := clearCreditsRateLimitKeys(ctx, repo, account, keys...)
	if err != nil {
		return false, err
	}
	return cleared, nil
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

func (s *AntigravityGatewayService) setCreditsRequestInsufficient(ctx context.Context, account *Account, creditBalance AICredit) {
	if account == nil || account.ID == 0 {
		return
	}
	now := time.Now().UTC()
	resetAt := now.Add(creditsRequestInsufficientCooldown)

	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	rawLimits, _ := account.Extra[modelRateLimitsKey].(map[string]any)
	if rawLimits == nil {
		rawLimits = make(map[string]any)
	}
	delete(rawLimits, creditsExhaustedKey)
	rawLimits[creditsRequestInsufficientKey] = map[string]any{
		"rate_limited_at":          now.Format(time.RFC3339),
		"rate_limit_reset_at":      resetAt.Format(time.RFC3339),
		creditsAmountField:         creditBalance.Amount,
		creditsMinimumBalanceField: creditBalance.MinimumBalance,
		creditsReasonField:         creditsRequestInsufficientReason,
	}
	account.Extra[modelRateLimitsKey] = rawLimits

	if s.accountRepo != nil {
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
			modelRateLimitsKey: rawLimits,
		}); err != nil {
			logger.LegacyPrintf("service.antigravity_gateway", "set credits request insufficient failed: account=%d err=%v", account.ID, err)
			return
		}
	}
	if s.schedulerSnapshot != nil {
		if err := s.schedulerSnapshot.UpdateAccountInCache(ctx, account); err != nil {
			logger.LegacyPrintf("service.antigravity_gateway", "cache update credits request insufficient failed: account=%d err=%v", account.ID, err)
		}
	}
	logger.LegacyPrintf("service.antigravity_gateway", "credits_request_insufficient_marked account=%d reset_at=%s amount=%.4f minimum=%.4f",
		account.ID, resetAt.Format(time.RFC3339), creditBalance.Amount, creditBalance.MinimumBalance)
}

func (s *AntigravityGatewayService) setCreditsOveragesActive(ctx context.Context, account *Account, modelKey string, waitDuration time.Duration) {
	if account == nil || account.ID == 0 {
		return
	}
	extraKey := buildAntigravityCreditsOveragesExtraKey(modelKey)
	if extraKey == "" {
		return
	}
	now := time.Now().UTC()
	activeUntil := now.Add(resolveCreditsOveragesActiveDuration(waitDuration))
	state := map[string]any{
		creditsActivatedAtField:   now.Format(time.RFC3339),
		creditsLastSuccessAtField: now.Format(time.RFC3339),
		creditsActiveUntilField:   activeUntil.Format(time.RFC3339),
		creditsReasonField:        creditsOveragesActiveReason,
	}

	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[extraKey] = state

	if s.accountRepo != nil {
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
			extraKey: state,
		}); err != nil {
			logger.LegacyPrintf("service.antigravity_gateway", "set credits overages active failed: account=%d model=%s err=%v", account.ID, modelKey, err)
			return
		}
	}
	if s.schedulerSnapshot != nil {
		if err := s.schedulerSnapshot.UpdateAccountInCache(ctx, account); err != nil {
			logger.LegacyPrintf("service.antigravity_gateway", "cache update credits overages active failed: account=%d model=%s err=%v", account.ID, modelKey, err)
		}
	}
	logger.LegacyPrintf("service.antigravity_gateway", "credits_overages_active_marked account=%d model=%s active_until=%s",
		account.ID, modelKey, activeUntil.Format(time.RFC3339))
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
		s.setCreditsOveragesActive(p.ctx, p.account, modelKey, waitDuration)
		if s.cache != nil && p.sessionHash != "" {
			_ = s.cache.DeleteSessionAccountID(p.ctx, p.groupID, p.sessionHash)
		}
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
			s.setCreditsRequestInsufficient(ctx, account, creditBalance)
			logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s account=%d marked_request_insufficient=true status=%d confirmed_credits_available=true amount=%.4f minimum=%.4f body=%s",
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
