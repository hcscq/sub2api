//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type antigravitySelectionTouchCache struct {
	snapshot []*Account
	accounts map[int64]*Account
	updates  map[int64]time.Time
}

func (c *antigravitySelectionTouchCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return c.snapshot, true, nil
}

func (c *antigravitySelectionTouchCache) SetSnapshot(context.Context, SchedulerBucket, []Account) error {
	return nil
}

func (c *antigravitySelectionTouchCache) GetAccount(_ context.Context, accountID int64) (*Account, error) {
	if c.accounts == nil {
		return nil, nil
	}
	return c.accounts[accountID], nil
}

func (c *antigravitySelectionTouchCache) SetAccount(_ context.Context, account *Account) error {
	if account == nil {
		return nil
	}
	if c.accounts == nil {
		c.accounts = make(map[int64]*Account)
	}
	c.accounts[account.ID] = account
	return nil
}

func (c *antigravitySelectionTouchCache) DeleteAccount(context.Context, int64) error {
	return nil
}

func (c *antigravitySelectionTouchCache) UpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	if c.updates == nil {
		c.updates = make(map[int64]time.Time)
	}
	for accountID, usedAt := range updates {
		c.updates[accountID] = usedAt
		if account, ok := c.accounts[accountID]; ok && account != nil {
			account.LastUsedAt = ptr(usedAt)
		}
		for _, account := range c.snapshot {
			if account != nil && account.ID == accountID {
				account.LastUsedAt = ptr(usedAt)
			}
		}
	}
	return nil
}

func (c *antigravitySelectionTouchCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

func (c *antigravitySelectionTouchCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return nil, nil
}

func (c *antigravitySelectionTouchCache) GetOutboxWatermark(context.Context) (int64, error) {
	return 0, nil
}

func (c *antigravitySelectionTouchCache) SetOutboxWatermark(context.Context, int64) error {
	return nil
}

func TestGatewayService_MarkAntigravitySelectionAttemptUpdatesSchedulerCacheAndDeferredQueue(t *testing.T) {
	account := &Account{
		ID:          11,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
	}
	cache := &antigravitySelectionTouchCache{
		accounts: map[int64]*Account{
			account.ID: account,
		},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	deferred := &DeferredService{}
	svc := &GatewayService{
		schedulerSnapshot: schedulerSnapshot,
		deferredService:   deferred,
	}

	svc.MarkAntigravitySelectionAttempt(context.Background(), account)

	updatedAt, ok := cache.updates[account.ID]
	require.True(t, ok, "scheduler cache should receive last_used update")
	require.False(t, updatedAt.IsZero())
	require.NotNil(t, account.LastUsedAt, "account cache view should be updated immediately")

	value, ok := deferred.lastUsedUpdates.Load(account.ID)
	require.True(t, ok, "deferred DB update should be scheduled")
	queuedAt, ok := value.(time.Time)
	require.True(t, ok, "queued deferred update should store a timestamp")
	require.False(t, queuedAt.IsZero())
}

func TestGatewayService_SelectAccountForModelWithExclusions_AntigravityAttemptTouchChangesNextPick(t *testing.T) {
	now := time.Now()
	first := &Account{
		ID:          1,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-2 * time.Hour)),
	}
	second := &Account{
		ID:          2,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-1 * time.Hour)),
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{first, second},
		accounts: map[int64]*Account{
			first.ID:  first,
			second.ID: second,
		},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	svc := &GatewayService{
		schedulerSnapshot: schedulerSnapshot,
		deferredService:   &DeferredService{},
		cache:             &mockGatewayCacheForPlatform{},
		cfg:               testConfig(),
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	selectedFirst, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	require.NotNil(t, selectedFirst)
	require.Equal(t, int64(1), selectedFirst.ID, "older warm account should win before the attempt is touched")

	svc.MarkAntigravitySelectionAttempt(ctx, selectedFirst)

	selectedSecond, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	require.NotNil(t, selectedSecond)
	require.Equal(t, int64(2), selectedSecond.ID, "after touching the first attempt, the older healthy candidate should be preferred next")
}

func TestGatewayService_SelectAccountForModelWithExclusions_AntigravityWarmCandidateBeatsNeverUsedColdCandidate(t *testing.T) {
	now := time.Now()
	cold := &Account{
		ID:          11,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
	}
	warm := &Account{
		ID:          12,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-90 * time.Minute)),
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{cold, warm},
		accounts: map[int64]*Account{
			cold.ID: cold,
			warm.ID: warm,
		},
	}
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil),
		cache:             &mockGatewayCacheForPlatform{},
		cfg:               testConfig(),
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	selected, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, warm.ID, selected.ID, "warm antigravity account should beat never-used cold candidate")
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityFailurePenaltyBeatsLowerLoad(t *testing.T) {
	bad := &Account{
		ID:          21,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
	}
	good := &Account{
		ID:          22,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{bad, good},
		accounts: map[int64]*Account{
			bad.ID:  bad,
			good.ID: good,
		},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			bad.ID:  {AccountID: bad.ID, LoadRate: 0},
			good.ID: {AccountID: good.ID, LoadRate: 10},
		},
		skipDefaultLoad: true,
	}
	svc := &GatewayService{
		schedulerSnapshot:  schedulerSnapshot,
		concurrencyService: NewConcurrencyService(concurrencyCache),
		antigravityRuntime: newAntigravityAccountRuntimeStats(),
		cache:              &mockGatewayCacheForPlatform{},
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	svc.ReportAntigravityResult(bad.ID, false, nil)

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-sonnet-4-5", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, good.ID, result.Account.ID, "recently failed antigravity account should lose priority even when its load is lower")
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityWarmCandidateBeatsColdSlightlyLowerLoad(t *testing.T) {
	now := time.Now()
	cold := &Account{
		ID:          23,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
	}
	warm := &Account{
		ID:          24,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-30 * time.Minute)),
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{cold, warm},
		accounts: map[int64]*Account{
			cold.ID: cold,
			warm.ID: warm,
		},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			cold.ID: {AccountID: cold.ID, LoadRate: 5},
			warm.ID: {AccountID: warm.ID, LoadRate: 10},
		},
		skipDefaultLoad: true,
	}
	svc := &GatewayService{
		schedulerSnapshot:  schedulerSnapshot,
		concurrencyService: NewConcurrencyService(concurrencyCache),
		cache:              &mockGatewayCacheForPlatform{},
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-sonnet-4-5", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, warm.ID, result.Account.ID, "warm antigravity account should still win when the load gap is small")
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityColdCandidateBeatsHotWarmAccount(t *testing.T) {
	now := time.Now()
	cold := &Account{
		ID:          25,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
	}
	warm := &Account{
		ID:          26,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-30 * time.Minute)),
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{cold, warm},
		accounts: map[int64]*Account{
			cold.ID: cold,
			warm.ID: warm,
		},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			cold.ID: {AccountID: cold.ID, LoadRate: 0},
			warm.ID: {AccountID: warm.ID, LoadRate: 40},
		},
		skipDefaultLoad: true,
	}
	svc := &GatewayService{
		schedulerSnapshot:  schedulerSnapshot,
		concurrencyService: NewConcurrencyService(concurrencyCache),
		cache:              &mockGatewayCacheForPlatform{},
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-sonnet-4-5", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, cold.ID, result.Account.ID, "cold antigravity account should be used once warm accounts become materially hotter")
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityDirectCandidateBeatsCreditsFallback(t *testing.T) {
	now := time.Now()
	creditsFallback := &Account{
		ID:          31,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		Extra: map[string]any{
			"allow_overages": true,
			modelRateLimitsKey: map[string]any{
				"claude-sonnet-4-6": map[string]any{
					"rate_limited_at":     now.UTC().Format(time.RFC3339),
					"rate_limit_reset_at": now.Add(30 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}
	direct := &Account{
		ID:          32,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-1 * time.Hour)),
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{creditsFallback, direct},
		accounts: map[int64]*Account{
			creditsFallback.ID: creditsFallback,
			direct.ID:          direct,
		},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	concurrencyCache := mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			creditsFallback.ID: {AccountID: creditsFallback.ID, LoadRate: 0},
			direct.ID:          {AccountID: direct.ID, LoadRate: 0},
		},
	}
	svc := &GatewayService{
		schedulerSnapshot:  schedulerSnapshot,
		concurrencyService: NewConcurrencyService(&concurrencyCache),
		cache:              &mockGatewayCacheForPlatform{},
		cfg:                testConfig(),
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-sonnet-4-6", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, direct.ID, result.Account.ID, "when a direct antigravity account is available, credits fallback candidates should not win selection")
}

func TestGatewayService_SelectAccountForModelWithExclusions_AntigravityDirectCandidateBeatsCreditsFallback(t *testing.T) {
	now := time.Now()
	creditsFallback := &Account{
		ID:          41,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		Extra: map[string]any{
			"allow_overages": true,
			modelRateLimitsKey: map[string]any{
				"claude-sonnet-4-6": map[string]any{
					"rate_limited_at":     now.UTC().Format(time.RFC3339),
					"rate_limit_reset_at": now.Add(30 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}
	direct := &Account{
		ID:          42,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-1 * time.Hour)),
	}
	cache := &antigravitySelectionTouchCache{
		snapshot: []*Account{creditsFallback, direct},
		accounts: map[int64]*Account{
			creditsFallback.ID: creditsFallback,
			direct.ID:          direct,
		},
	}
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil),
		cache:             &mockGatewayCacheForPlatform{},
		cfg:               testConfig(),
	}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	selected, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "claude-sonnet-4-6", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, direct.ID, selected.ID, "legacy antigravity selection should also prefer direct accounts before credits fallback candidates")
}
