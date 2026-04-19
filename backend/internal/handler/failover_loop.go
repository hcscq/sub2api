package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// TempUnscheduler 用于 HandleFailoverError 中同账号重试耗尽后的临时封禁。
// GatewayService 隐式实现此接口。
type TempUnscheduler interface {
	TempUnscheduleRetryableError(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError)
}

// FailoverAction 表示 failover 错误处理后的下一步动作
type FailoverAction int

const (
	// FailoverContinue 继续循环（同账号重试或切换账号，调用方统一 continue）
	FailoverContinue FailoverAction = iota
	// FailoverExhausted 切换次数耗尽（调用方应返回错误响应）
	FailoverExhausted
	// FailoverCanceled context 已取消（调用方应直接 return）
	FailoverCanceled
)

const (
	// maxSameAccountRetries 同账号重试次数上限（针对 RetryableOnSameAccount 错误）
	maxSameAccountRetries = 3
	// sameAccountRetryDelay 同账号重试间隔
	sameAccountRetryDelay = 500 * time.Millisecond
	// singleAccountBackoffDelay 单账号分组 503 退避重试固定延时。
	// Service 层在 SingleAccountRetry 模式下已做充分原地重试（最多 3 次、总等待 30s），
	// Handler 层只需短暂间隔后重新进入 Service 层即可。
	singleAccountBackoffDelay = 2 * time.Second
)

// AntigravityFastFailoverBudget 控制 Antigravity 快速 failover 的额外探索窗口。
// 仅在多次上游快速失败且总耗时仍落在窗口内时，允许突破基础切号上限/循环重试候选池。
type AntigravityFastFailoverBudget struct {
	TotalWindow       time.Duration
	FastFailThreshold time.Duration
	RecycleDelay      time.Duration
	MaxExtraSwitches  int
}

func (b AntigravityFastFailoverBudget) enabled() bool {
	return b.TotalWindow > 0 && b.FastFailThreshold > 0 && b.MaxExtraSwitches > 0
}

// FailoverState 跨循环迭代共享的 failover 状态
type FailoverState struct {
	SwitchCount              int
	MaxSwitches              int
	FailedAccountIDs         map[int64]struct{}
	SameAccountRetryCount    map[int64]int
	LastFailoverErr          *service.UpstreamFailoverError
	ForceCacheBilling        bool
	hasBoundSession          bool
	singleAccountBackoff     bool
	modelCapacitySwitches    int
	modelCapacitySwitchLimit int
	startedAt                time.Time
	antigravityBudget        AntigravityFastFailoverBudget
	antigravityBudgetSeen    bool
	antigravityBudgetFast    bool
	antigravityExtraUsed     int
}

// NewFailoverState 创建 failover 状态
func NewFailoverState(maxSwitches int, hasBoundSession bool) *FailoverState {
	return &FailoverState{
		MaxSwitches:              maxSwitches,
		FailedAccountIDs:         make(map[int64]struct{}),
		SameAccountRetryCount:    make(map[int64]int),
		hasBoundSession:          hasBoundSession,
		modelCapacitySwitchLimit: config.DefaultGatewayAntigravityModelCapacitySwitchLimit,
		startedAt:                time.Now(),
		antigravityBudgetFast:    true,
	}
}

// SetSingleAccountBackoffEnabled 控制是否允许 selection exhausted 走单账号 503 回退。
// 仅应在 Antigravity 单账号分组场景开启。
func (s *FailoverState) SetSingleAccountBackoffEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.singleAccountBackoff = enabled
}

// SetAntigravityModelCapacitySwitchLimit 配置 Antigravity MODEL_CAPACITY_EXHAUSTED 跨账号切换上限。
func (s *FailoverState) SetAntigravityModelCapacitySwitchLimit(limit int) {
	if s == nil {
		return
	}
	if limit <= 0 {
		limit = config.DefaultGatewayAntigravityModelCapacitySwitchLimit
	}
	s.modelCapacitySwitchLimit = limit
}

// SetAntigravityFastFailoverBudget 配置 Antigravity 快速 failover 的额外时间预算。
func (s *FailoverState) SetAntigravityFastFailoverBudget(budget AntigravityFastFailoverBudget) {
	if s == nil {
		return
	}
	if budget.RecycleDelay < 0 {
		budget.RecycleDelay = 0
	}
	s.antigravityBudget = budget
}

// HandleFailoverError 处理 UpstreamFailoverError，返回下一步动作。
// 兼容旧调用方：未提供单次尝试耗时，按 0 处理。
func (s *FailoverState) HandleFailoverError(
	ctx context.Context,
	gatewayService TempUnscheduler,
	accountID int64,
	platform string,
	failoverErr *service.UpstreamFailoverError,
) FailoverAction {
	return s.HandleFailoverErrorWithDuration(ctx, gatewayService, accountID, platform, failoverErr, 0)
}

// HandleFailoverErrorWithDuration 处理 UpstreamFailoverError，返回下一步动作。
// 包含：缓存计费判断、同账号重试、临时封禁、切换计数、Antigravity 延时。
func (s *FailoverState) HandleFailoverErrorWithDuration(
	ctx context.Context,
	gatewayService TempUnscheduler,
	accountID int64,
	platform string,
	failoverErr *service.UpstreamFailoverError,
	attemptDuration time.Duration,
) FailoverAction {
	s.LastFailoverErr = failoverErr

	// 缓存计费判断
	if needForceCacheBilling(s.hasBoundSession, failoverErr) {
		s.ForceCacheBilling = true
	}

	// Antigravity 的 MODEL_CAPACITY_EXHAUSTED 在多账号场景下走有限跨账号切换。
	// 单账号场景维持原有外层回退链路。
	if failoverErr.ModelCapacityExhausted {
		logger.FromContext(ctx).Warn("gateway.failover_model_capacity_exhausted",
			zap.Int64("account_id", accountID),
			zap.Int("upstream_status", failoverErr.StatusCode),
			zap.Bool("single_account_backoff", s.singleAccountBackoff),
		)
		if !s.singleAccountBackoff {
			s.FailedAccountIDs[accountID] = struct{}{}
			s.observeAntigravityBudget(platform, failoverErr, attemptDuration)
			allowExtraSwitch := false
			if s.modelCapacitySwitches >= s.modelCapacitySwitchLimit || s.SwitchCount >= s.MaxSwitches {
				allowExtraSwitch = s.tryUseAntigravityExtraSwitch(ctx, failoverErr, attemptDuration, "model_capacity")
			}
			if !allowExtraSwitch && (s.modelCapacitySwitches >= s.modelCapacitySwitchLimit || s.SwitchCount >= s.MaxSwitches) {
				return FailoverExhausted
			}
			s.modelCapacitySwitches++
			s.SwitchCount++
			logger.FromContext(ctx).Warn("gateway.failover_model_capacity_switch_account",
				zap.Int64("account_id", accountID),
				zap.Int("model_capacity_switch_count", s.modelCapacitySwitches),
				zap.Int("model_capacity_switch_max", s.modelCapacitySwitchLimit),
				zap.Int("switch_count", s.SwitchCount),
				zap.Int("max_switches", s.MaxSwitches),
			)
			return FailoverContinue
		}
	}

	// 同账号重试：对 RetryableOnSameAccount 的临时性错误，先在同一账号上重试
	if failoverErr.RetryableOnSameAccount && s.SameAccountRetryCount[accountID] < maxSameAccountRetries {
		s.SameAccountRetryCount[accountID]++
		logger.FromContext(ctx).Warn("gateway.failover_same_account_retry",
			zap.Int64("account_id", accountID),
			zap.Int("upstream_status", failoverErr.StatusCode),
			zap.Int("same_account_retry_count", s.SameAccountRetryCount[accountID]),
			zap.Int("same_account_retry_max", maxSameAccountRetries),
		)
		if !sleepWithContext(ctx, sameAccountRetryDelay) {
			return FailoverCanceled
		}
		return FailoverContinue
	}

	// 同账号重试用尽，执行临时封禁
	if failoverErr.RetryableOnSameAccount {
		gatewayService.TempUnscheduleRetryableError(ctx, accountID, failoverErr)
	}

	// 加入失败列表
	s.FailedAccountIDs[accountID] = struct{}{}
	s.observeAntigravityBudget(platform, failoverErr, attemptDuration)

	// 检查是否耗尽
	if s.SwitchCount >= s.MaxSwitches && !s.tryUseAntigravityExtraSwitch(ctx, failoverErr, attemptDuration, "switch_limit") {
		return FailoverExhausted
	}

	// 递增切换计数
	s.SwitchCount++
	logger.FromContext(ctx).Warn("gateway.failover_switch_account",
		zap.Int64("account_id", accountID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("switch_count", s.SwitchCount),
		zap.Int("max_switches", s.MaxSwitches),
	)

	// Antigravity 平台换号线性递增延时
	if platform == service.PlatformAntigravity {
		delay := time.Duration(s.SwitchCount-1) * time.Second
		if failoverErr.ModelCapacityExhausted {
			// Service 层对 MODEL_CAPACITY_EXHAUSTED 已做 1s*3 次原地重试；
			// Handler 层继续换号时不再叠加线性等待，优先尽快探索其他候选。
			delay = 0
		}
		if !sleepWithContext(ctx, delay) {
			return FailoverCanceled
		}
	}

	return FailoverContinue
}

// HandleSelectionExhausted 处理选号失败（所有候选账号都在排除列表中）时的退避重试决策。
// 仅针对 Antigravity 单账号分组的 503 场景：
// 清除排除列表、等待退避后重新选号。
//
// 使用前必须先调用 SetSingleAccountBackoffEnabled(true)。
//
// 返回 FailoverContinue 时，调用方应设置 SingleAccountRetry context 并 continue。
// 返回 FailoverExhausted 时，调用方应返回错误响应。
// 返回 FailoverCanceled 时，调用方应直接 return。
func (s *FailoverState) HandleSelectionExhausted(ctx context.Context) FailoverAction {
	if s.singleAccountBackoff &&
		s.LastFailoverErr != nil &&
		s.LastFailoverErr.StatusCode == http.StatusServiceUnavailable &&
		s.SwitchCount < s.MaxSwitches {

		logger.FromContext(ctx).Warn("gateway.failover_single_account_backoff",
			zap.Duration("backoff_delay", singleAccountBackoffDelay),
			zap.Int("switch_count", s.SwitchCount),
			zap.Int("max_switches", s.MaxSwitches),
		)
		if !sleepWithContext(ctx, singleAccountBackoffDelay) {
			return FailoverCanceled
		}
		logger.FromContext(ctx).Warn("gateway.failover_single_account_retry",
			zap.Int("switch_count", s.SwitchCount),
			zap.Int("max_switches", s.MaxSwitches),
		)
		s.FailedAccountIDs = make(map[int64]struct{})
		return FailoverContinue
	}
	if s.canRecycleAntigravityCandidates() {
		logger.FromContext(ctx).Warn("gateway.failover_fast_failover_recycle_candidates",
			zap.Duration("elapsed", time.Since(s.startedAt).Truncate(time.Millisecond)),
			zap.Duration("recycle_delay", s.antigravityBudget.RecycleDelay),
			zap.Int("failed_accounts", len(s.FailedAccountIDs)),
			zap.Int("switch_count", s.SwitchCount),
			zap.Int("max_switches", s.MaxSwitches),
			zap.Int("extra_switches_used", s.antigravityExtraUsed),
			zap.Int("extra_switches_max", s.antigravityBudget.MaxExtraSwitches),
		)
		if !sleepWithContext(ctx, s.antigravityBudget.RecycleDelay) {
			return FailoverCanceled
		}
		s.FailedAccountIDs = make(map[int64]struct{})
		return FailoverContinue
	}
	return FailoverExhausted
}

// needForceCacheBilling 判断 failover 时是否需要强制缓存计费。
// 粘性会话切换账号、或上游明确标记时，将 input_tokens 转为 cache_read 计费。
func needForceCacheBilling(hasBoundSession bool, failoverErr *service.UpstreamFailoverError) bool {
	return hasBoundSession || (failoverErr != nil && failoverErr.ForceCacheBilling)
}

// sleepWithContext 等待指定时长，返回 false 表示 context 已取消。
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (s *FailoverState) observeAntigravityBudget(platform string, failoverErr *service.UpstreamFailoverError, attemptDuration time.Duration) {
	if s == nil || platform != service.PlatformAntigravity || failoverErr == nil || !s.antigravityBudget.enabled() {
		return
	}
	if !s.antigravityBudgetSeen {
		s.antigravityBudgetSeen = true
		s.antigravityBudgetFast = true
	}
	if !isAntigravityFastFailoverCandidate(failoverErr) || attemptDuration > s.antigravityBudget.FastFailThreshold {
		s.antigravityBudgetFast = false
	}
}

func (s *FailoverState) canUseAntigravityBudget() bool {
	if s == nil || !s.antigravityBudget.enabled() || !s.antigravityBudgetSeen || !s.antigravityBudgetFast {
		return false
	}
	return time.Since(s.startedAt) <= s.antigravityBudget.TotalWindow
}

func (s *FailoverState) tryUseAntigravityExtraSwitch(
	ctx context.Context,
	failoverErr *service.UpstreamFailoverError,
	attemptDuration time.Duration,
	reason string,
) bool {
	if s == nil || failoverErr == nil || !s.canUseAntigravityBudget() {
		return false
	}
	if s.antigravityExtraUsed >= s.antigravityBudget.MaxExtraSwitches {
		return false
	}
	s.antigravityExtraUsed++
	logger.FromContext(ctx).Warn("gateway.failover_fast_failover_extend",
		zap.String("reason", reason),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Bool("model_capacity_exhausted", failoverErr.ModelCapacityExhausted),
		zap.Duration("attempt_duration", attemptDuration.Truncate(time.Millisecond)),
		zap.Duration("elapsed", time.Since(s.startedAt).Truncate(time.Millisecond)),
		zap.Int("switch_count", s.SwitchCount),
		zap.Int("max_switches", s.MaxSwitches),
		zap.Int("extra_switches_used", s.antigravityExtraUsed),
		zap.Int("extra_switches_max", s.antigravityBudget.MaxExtraSwitches),
	)
	return true
}

func (s *FailoverState) canRecycleAntigravityCandidates() bool {
	if s == nil || !s.canUseAntigravityBudget() || s.LastFailoverErr == nil {
		return false
	}
	if s.SwitchCount >= s.MaxSwitches && s.antigravityExtraUsed >= s.antigravityBudget.MaxExtraSwitches {
		return false
	}
	if s.LastFailoverErr.ModelCapacityExhausted &&
		s.modelCapacitySwitches >= s.modelCapacitySwitchLimit &&
		s.antigravityExtraUsed >= s.antigravityBudget.MaxExtraSwitches {
		return false
	}
	if !isAntigravityFastFailoverCandidate(s.LastFailoverErr) {
		return false
	}
	return len(s.FailedAccountIDs) > 0
}

func isAntigravityFastFailoverCandidate(failoverErr *service.UpstreamFailoverError) bool {
	if failoverErr == nil {
		return false
	}
	if failoverErr.ModelCapacityExhausted {
		return true
	}
	switch failoverErr.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		529:
		return true
	default:
		return false
	}
}
