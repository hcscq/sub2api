package service

import (
	"context"
	"strings"
	"sync"
	"time"
)

const antigravityModelCapacityAccountCooldown = 3 * time.Second

type antigravityModelCapacityCooldownKey struct {
	AccountID int64
	ModelKey  string
}

var (
	antigravityModelCapacityCooldownMu    sync.RWMutex
	antigravityModelCapacityCooldownUntil = make(map[antigravityModelCapacityCooldownKey]time.Time)
)

func normalizeAntigravityModelCapacityCooldownKey(ctx context.Context, account *Account, requestedModel string) string {
	if account == nil {
		return ""
	}
	modelKey := resolveFinalAntigravityModelKey(ctx, account, requestedModel)
	if strings.TrimSpace(modelKey) == "" {
		modelKey = resolveAntigravityModelKey(requestedModel)
	}
	return normalizeAntigravityModelName(modelKey)
}

func antigravityModelCapacityCooldownRemaining(accountID int64, modelKey string) time.Duration {
	modelKey = normalizeAntigravityModelName(modelKey)
	if accountID == 0 || modelKey == "" {
		return 0
	}

	key := antigravityModelCapacityCooldownKey{
		AccountID: accountID,
		ModelKey:  modelKey,
	}

	antigravityModelCapacityCooldownMu.RLock()
	resetAt, ok := antigravityModelCapacityCooldownUntil[key]
	antigravityModelCapacityCooldownMu.RUnlock()
	if !ok {
		return 0
	}

	remaining := time.Until(resetAt)
	if remaining > 0 {
		return remaining
	}

	antigravityModelCapacityCooldownMu.Lock()
	delete(antigravityModelCapacityCooldownUntil, key)
	antigravityModelCapacityCooldownMu.Unlock()
	return 0
}

func setAntigravityModelCapacityCooldown(accountID int64, modelKey string, duration time.Duration) (time.Time, bool) {
	modelKey = normalizeAntigravityModelName(modelKey)
	if accountID == 0 || modelKey == "" {
		return time.Time{}, false
	}
	if duration <= 0 {
		duration = antigravityModelCapacityAccountCooldown
	}

	resetAt := time.Now().Add(duration)
	key := antigravityModelCapacityCooldownKey{
		AccountID: accountID,
		ModelKey:  modelKey,
	}

	antigravityModelCapacityCooldownMu.Lock()
	antigravityModelCapacityCooldownUntil[key] = resetAt
	antigravityModelCapacityCooldownMu.Unlock()
	return resetAt, true
}

func clearAntigravityModelCapacityCooldown(accountID int64, modelKey string) {
	modelKey = normalizeAntigravityModelName(modelKey)
	if accountID == 0 || modelKey == "" {
		return
	}

	key := antigravityModelCapacityCooldownKey{
		AccountID: accountID,
		ModelKey:  modelKey,
	}

	antigravityModelCapacityCooldownMu.Lock()
	delete(antigravityModelCapacityCooldownUntil, key)
	antigravityModelCapacityCooldownMu.Unlock()
}

func (a *Account) GetAntigravityModelCapacityCooldownRemainingWithContext(ctx context.Context, requestedModel string) time.Duration {
	if a == nil || a.Platform != PlatformAntigravity {
		return 0
	}
	return antigravityModelCapacityCooldownRemaining(a.ID, normalizeAntigravityModelCapacityCooldownKey(ctx, a, requestedModel))
}

func (a *Account) isAntigravityModelCapacityCoolingDownWithContext(ctx context.Context, requestedModel string) bool {
	return a.GetAntigravityModelCapacityCooldownRemainingWithContext(ctx, requestedModel) > 0
}
