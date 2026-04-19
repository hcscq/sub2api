package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildSchedulerMetadataAccount_PreservesAntigravityCreditsOveragesExtra(t *testing.T) {
	activeUntil := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)

	account := service.Account{
		ID:       425,
		Platform: service.PlatformAntigravity,
		Extra: map[string]any{
			"allow_overages": true,
			"antigravity_credits_overages": map[string]any{
				"state": "active",
			},
			"antigravity_credits_overages:claude-opus-4-6-thinking": map[string]any{
				"active_until": activeUntil,
			},
			"unused_large_field": "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.NotNil(t, got.Extra)
	require.Equal(t, true, got.Extra["allow_overages"])
	require.Equal(t, map[string]any{"state": "active"}, got.Extra["antigravity_credits_overages"])
	require.Equal(t, map[string]any{"active_until": activeUntil}, got.Extra["antigravity_credits_overages:claude-opus-4-6-thinking"])
	require.NotContains(t, got.Extra, "unused_large_field")
}
