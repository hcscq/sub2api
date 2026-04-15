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
	}
	second := &Account{
		ID:          2,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  ptr(now.Add(-2 * time.Hour)),
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
	require.Equal(t, int64(1), selectedFirst.ID, "nil last_used should still win before the attempt is touched")

	svc.MarkAntigravitySelectionAttempt(ctx, selectedFirst)

	selectedSecond, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "claude-sonnet-4-5", nil)
	require.NoError(t, err)
	require.NotNil(t, selectedSecond)
	require.Equal(t, int64(2), selectedSecond.ID, "after touching the first attempt, the older healthy candidate should be preferred next")
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
