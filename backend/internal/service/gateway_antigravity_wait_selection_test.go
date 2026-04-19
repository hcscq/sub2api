//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityWaitersDoNotMaskImmediateCapacity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 2,
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {
				AccountID:          1,
				CurrentConcurrency: 1,
				WaitingCount:       1,
				LoadRate:           100,
			},
		},
		acquireResults: map[int64]bool{1: true},
	}

	svc := &GatewayService{
		accountRepo:        repo,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Acquired, "still has a real slot, should try immediate acquire instead of entering wait plan")
	require.Nil(t, result.WaitPlan)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(1), result.Account.ID)
	require.GreaterOrEqual(t, concurrencyCache.acquireAccountCalls, 1)
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityWaitPlanPrefersShorterQueue(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	now := time.Now()

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				LastUsedAt:  ptr(now.Add(-2 * time.Hour)),
			},
			{
				ID:          2,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				LastUsedAt:  ptr(now.Add(-1 * time.Hour)),
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {
				AccountID:          1,
				CurrentConcurrency: 1,
				WaitingCount:       6,
				LoadRate:           700,
			},
			2: {
				AccountID:          2,
				CurrentConcurrency: 1,
				WaitingCount:       0,
				LoadRate:           100,
			},
		},
		acquireResults: map[int64]bool{
			1: false,
			2: false,
		},
	}

	svc := &GatewayService{
		accountRepo:        repo,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "wait plan should avoid the hottest queue instead of pinning everything to the oldest account")
	require.Equal(t, int64(2), result.WaitPlan.AccountID)
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityStickyBusyFallsThroughToImmediateBackup(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	sessionHash := "same-client-same-request"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
			{
				ID:          2,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {
				AccountID:          1,
				CurrentConcurrency: 1,
				WaitingCount:       0,
				LoadRate:           100,
			},
			2: {
				AccountID:          2,
				CurrentConcurrency: 0,
				WaitingCount:       0,
				LoadRate:           0,
			},
		},
		acquireResults: map[int64]bool{
			1: false,
			2: true,
		},
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{
			sessionHash: 1,
		},
	}

	svc := &GatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, sessionHash, "", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Acquired, "busy sticky account should not block an immediately available backup account")
	require.Nil(t, result.WaitPlan)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID)
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityStickyBusyFallsThroughToShorterWaitQueue(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	now := time.Now()
	sessionHash := "same-client-same-request"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				LastUsedAt:  ptr(now.Add(-2 * time.Hour)),
			},
			{
				ID:          2,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				LastUsedAt:  ptr(now.Add(-1 * time.Hour)),
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {
				AccountID:          1,
				CurrentConcurrency: 1,
				WaitingCount:       5,
				LoadRate:           600,
			},
			2: {
				AccountID:          2,
				CurrentConcurrency: 1,
				WaitingCount:       0,
				LoadRate:           100,
			},
		},
		acquireResults: map[int64]bool{
			1: false,
			2: false,
		},
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{
			sessionHash: 1,
		},
	}

	svc := &GatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, sessionHash, "", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "sticky wait should not pin antigravity to a longer queue when another account can drain faster")
	require.Equal(t, int64(2), result.WaitPlan.AccountID)
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityRoutedStickyBusyFallsThroughToImmediateBackup(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	groupID := int64(41)
	requestedModel := "claude-sonnet-4-5"
	sessionHash := "same-client-same-request"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
			{
				ID:          2,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:                  groupID,
				Name:                "route-group",
				Platform:            PlatformAnthropic,
				Status:              StatusActive,
				Hydrated:            true,
				ModelRoutingEnabled: true,
				ModelRouting: map[string][]int64{
					requestedModel: {1, 2},
				},
			},
		},
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {
				AccountID:          1,
				CurrentConcurrency: 1,
				WaitingCount:       0,
				LoadRate:           100,
			},
			2: {
				AccountID:          2,
				CurrentConcurrency: 0,
				WaitingCount:       0,
				LoadRate:           0,
			},
		},
		acquireResults: map[int64]bool{
			1: false,
			2: true,
		},
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{
			sessionHash: 1,
		},
	}

	svc := &GatewayService{
		accountRepo:        repo,
		groupRepo:          groupRepo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, requestedModel, nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Acquired, "routed sticky account should not block an immediately available backup account")
	require.Nil(t, result.WaitPlan)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID)
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityRoutedStickyBusyFallsThroughToShorterWaitQueue(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	groupID := int64(42)
	requestedModel := "claude-sonnet-4-5"
	now := time.Now()
	sessionHash := "same-client-same-request"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				LastUsedAt:  ptr(now.Add(-2 * time.Hour)),
			},
			{
				ID:          2,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				LastUsedAt:  ptr(now.Add(-1 * time.Hour)),
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:                  groupID,
				Name:                "route-group",
				Platform:            PlatformAnthropic,
				Status:              StatusActive,
				Hydrated:            true,
				ModelRoutingEnabled: true,
				ModelRouting: map[string][]int64{
					requestedModel: {1, 2},
				},
			},
		},
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {
				AccountID:          1,
				CurrentConcurrency: 1,
				WaitingCount:       5,
				LoadRate:           600,
			},
			2: {
				AccountID:          2,
				CurrentConcurrency: 1,
				WaitingCount:       0,
				LoadRate:           100,
			},
		},
		acquireResults: map[int64]bool{
			1: false,
			2: false,
		},
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{
			sessionHash: 1,
		},
	}

	svc := &GatewayService{
		accountRepo:        repo,
		groupRepo:          groupRepo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, requestedModel, nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID, "routed sticky wait should not pin antigravity to a longer queue when another routed account can drain faster")
	require.Equal(t, int64(2), result.WaitPlan.AccountID)
}

func TestGatewayService_SelectAccountWithLoadAwareness_AntigravityCreditsFallbackDoesNotBindSticky(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	sessionHash := "credits-fallback-session"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          425,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Extra: map[string]any{
					"allow_overages": true,
					"antigravity_credits_overages:claude-opus-4-6-thinking": map[string]any{
						"active_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cache := &mockGatewayCacheForPlatform{sessionBindings: make(map[string]int64)}
	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			425: {
				AccountID:          425,
				CurrentConcurrency: 0,
				WaitingCount:       0,
				LoadRate:           0,
			},
		},
		acquireResults: map[int64]bool{425: true},
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true

	svc := &GatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, sessionHash, "claude-opus-4-6", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Acquired)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(425), result.Account.ID)
	require.NotContains(t, cache.sessionBindings, sessionHash)

	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
}

func TestGatewayService_SelectAccountForModel_AntigravityCreditsFallbackDoesNotBindSticky(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	sessionHash := "legacy-credits-fallback-session"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          425,
				Platform:    PlatformAntigravity,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Extra: map[string]any{
					"allow_overages": true,
					"antigravity_credits_overages:claude-opus-4-6-thinking": map[string]any{
						"active_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cache := &mockGatewayCacheForPlatform{sessionBindings: make(map[string]int64)}

	svc := &GatewayService{
		accountRepo: repo,
		cache:       cache,
		cfg:         testConfig(),
	}

	account, err := svc.SelectAccountForModel(ctx, nil, sessionHash, "claude-opus-4-6")
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(425), account.ID)
	require.NotContains(t, cache.sessionBindings, sessionHash)
}
