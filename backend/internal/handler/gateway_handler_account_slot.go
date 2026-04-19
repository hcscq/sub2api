package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// acquireWaitPlannedAccountSlot handles account wait-queue accounting and sticky
// session binding for handlers that receive a scheduler wait plan.
func (h *GatewayHandler) acquireWaitPlannedAccountSlot(
	c *gin.Context,
	groupID *int64,
	sessionHash string,
	account *service.Account,
	requestedModel string,
	waitPlan *service.AccountWaitPlan,
	reqStream bool,
	streamStarted *bool,
	reqLog *zap.Logger,
	logPrefix string,
) (func(), bool, error) {
	if reqLog == nil {
		reqLog = zap.NewNop()
	}

	ctx := c.Request.Context()
	accountWaitCounted := false
	canWait, err := h.concurrencyHelper.IncrementAccountWaitCount(ctx, account.ID, waitPlan.MaxWaiting)
	if err != nil {
		reqLog.Warn(logPrefix+".account_wait_counter_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	} else if !canWait {
		reqLog.Info(logPrefix+".account_wait_queue_full",
			zap.Int64("account_id", account.ID),
			zap.Int("max_waiting", waitPlan.MaxWaiting),
		)
		return nil, true, nil
	}
	if err == nil && canWait {
		accountWaitCounted = true
	}

	releaseWait := func() {
		if accountWaitCounted {
			h.concurrencyHelper.DecrementAccountWaitCount(ctx, account.ID)
			accountWaitCounted = false
		}
	}
	defer releaseWait()

	accountReleaseFunc, err := h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
		c,
		account.ID,
		waitPlan.MaxConcurrency,
		waitPlan.Timeout,
		reqStream,
		streamStarted,
	)
	if err != nil {
		return nil, false, err
	}

	// Slot acquired: no longer waiting in queue.
	releaseWait()
	if h.gatewayService != nil {
		if h.gatewayService.ShouldBindStickySession(account, requestedModel) {
			if err := h.gatewayService.BindStickySession(ctx, groupID, sessionHash, account.ID); err != nil {
				reqLog.Warn(logPrefix+".bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		}
	}
	return accountReleaseFunc, false, nil
}
