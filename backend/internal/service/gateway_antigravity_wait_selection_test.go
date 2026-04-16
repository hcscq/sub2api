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
