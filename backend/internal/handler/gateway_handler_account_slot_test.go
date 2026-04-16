//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type accountWaitTrackingCache struct {
	helperConcurrencyCacheStub
	accountWaitIncrementCalls int
	accountWaitDecrementCalls int
	lastWaitAccountID         int64
	lastMaxWaiting            int
}

func (s *accountWaitTrackingCache) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	s.accountWaitIncrementCalls++
	s.lastWaitAccountID = accountID
	s.lastMaxWaiting = maxWait
	return true, nil
}

func (s *accountWaitTrackingCache) DecrementAccountWaitCount(ctx context.Context, accountID int64) error {
	s.accountWaitDecrementCalls++
	return nil
}

type gatewayStickyCacheStub struct {
	sessionBindings map[string]int64
}

func (s *gatewayStickyCacheStub) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if s == nil || s.sessionBindings == nil {
		return 0, nil
	}
	return s.sessionBindings[sessionHash], nil
}

func (s *gatewayStickyCacheStub) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	if s.sessionBindings == nil {
		s.sessionBindings = make(map[string]int64)
	}
	s.sessionBindings[sessionHash] = accountID
	return nil
}

func (s *gatewayStickyCacheStub) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

func (s *gatewayStickyCacheStub) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	delete(s.sessionBindings, sessionHash)
	return nil
}

func TestGatewayHandlerAcquireWaitPlannedAccountSlot_CountsQueueAndBindsStickySession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	concurrencyCache := &accountWaitTrackingCache{
		helperConcurrencyCacheStub: helperConcurrencyCacheStub{
			accountSeq: []bool{false, true},
		},
	}
	concurrencySvc := service.NewConcurrencyService(concurrencyCache)

	stickyCache := &gatewayStickyCacheStub{sessionBindings: make(map[string]int64)}
	gatewaySvc := service.NewGatewayService(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		stickyCache,
		nil,
		nil,
		concurrencySvc,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	h := &GatewayHandler{
		gatewayService:    gatewaySvc,
		concurrencyHelper: NewConcurrencyHelper(concurrencySvc, SSEPingFormatClaude, 0),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	groupID := int64(42)
	sessionHash := "sticky-session"
	account := &service.Account{
		ID:          1001,
		Concurrency: 1,
	}
	waitPlan := &service.AccountWaitPlan{
		AccountID:      account.ID,
		MaxConcurrency: account.Concurrency,
		Timeout:        time.Second,
		MaxWaiting:     3,
	}
	streamStarted := false

	releaseFunc, queueFull, err := h.acquireWaitPlannedAccountSlot(
		c,
		&groupID,
		sessionHash,
		account,
		waitPlan,
		false,
		&streamStarted,
		zap.NewNop(),
		"gateway.cc",
	)

	require.NoError(t, err)
	require.False(t, queueFull)
	require.NotNil(t, releaseFunc)
	require.Equal(t, 1, concurrencyCache.accountWaitIncrementCalls)
	require.Equal(t, 1, concurrencyCache.accountWaitDecrementCalls)
	require.Equal(t, account.ID, concurrencyCache.lastWaitAccountID)
	require.Equal(t, waitPlan.MaxWaiting, concurrencyCache.lastMaxWaiting)
	require.Equal(t, account.ID, stickyCache.sessionBindings[sessionHash])

	releaseFunc()
	require.Equal(t, 1, concurrencyCache.accountReleaseCalls)
}
