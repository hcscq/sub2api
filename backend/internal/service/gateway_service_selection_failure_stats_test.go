package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCollectSelectionFailureStats(t *testing.T) {
	svc := &GatewayService{}
	model := "gpt-5.4"
	resetAt := time.Now().Add(2 * time.Minute).Format(time.RFC3339)

	accounts := []Account{
		// excluded
		{
			ID:          1,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
		},
		// unschedulable
		{
			ID:          2,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: false,
		},
		// platform filtered
		{
			ID:          3,
			Platform:    PlatformAntigravity,
			Status:      StatusActive,
			Schedulable: true,
		},
		// model unsupported
		{
			ID:          4,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-image": "gpt-image",
				},
			},
		},
		// model rate limited
		{
			ID:          5,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					model: map[string]any{
						"rate_limit_reset_at": resetAt,
					},
				},
			},
		},
		// eligible
		{
			ID:          6,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Schedulable: true,
		},
	}

	excluded := map[int64]struct{}{1: {}}
	stats := svc.collectSelectionFailureStats(context.Background(), accounts, model, PlatformOpenAI, excluded, false)

	if stats.Total != 6 {
		t.Fatalf("total=%d want=6", stats.Total)
	}
	if stats.Excluded != 1 {
		t.Fatalf("excluded=%d want=1", stats.Excluded)
	}
	if stats.Unschedulable != 1 {
		t.Fatalf("unschedulable=%d want=1", stats.Unschedulable)
	}
	if stats.PlatformFiltered != 1 {
		t.Fatalf("platform_filtered=%d want=1", stats.PlatformFiltered)
	}
	if stats.ModelUnsupported != 1 {
		t.Fatalf("model_unsupported=%d want=1", stats.ModelUnsupported)
	}
	if stats.ModelRateLimited != 1 {
		t.Fatalf("model_rate_limited=%d want=1", stats.ModelRateLimited)
	}
	if stats.Eligible != 1 {
		t.Fatalf("eligible=%d want=1", stats.Eligible)
	}
}

func TestDiagnoseSelectionFailure_UnschedulableDetail(t *testing.T) {
	svc := &GatewayService{}
	acc := &Account{
		ID:          7,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: false,
	}

	diagnosis := svc.diagnoseSelectionFailure(context.Background(), acc, "gpt-5.4", PlatformOpenAI, map[int64]struct{}{}, false)
	if diagnosis.Category != "unschedulable" {
		t.Fatalf("category=%s want=unschedulable", diagnosis.Category)
	}
	if diagnosis.Detail != "generic_unschedulable" {
		t.Fatalf("detail=%s want=generic_unschedulable", diagnosis.Detail)
	}
}

func TestDiagnoseSelectionFailure_ModelRateLimitedDetail(t *testing.T) {
	svc := &GatewayService{}
	model := "gpt-5.4"
	resetAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	acc := &Account{
		ID:          8,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				model: map[string]any{
					"rate_limit_reset_at": resetAt,
				},
			},
		},
	}

	diagnosis := svc.diagnoseSelectionFailure(context.Background(), acc, model, PlatformOpenAI, map[int64]struct{}{}, false)
	if diagnosis.Category != "model_rate_limited" {
		t.Fatalf("category=%s want=model_rate_limited", diagnosis.Category)
	}
	if !strings.Contains(diagnosis.Detail, "remaining=") {
		t.Fatalf("detail=%s want contains remaining=", diagnosis.Detail)
	}
}

func TestBuildAntigravitySelectionTraceCandidates(t *testing.T) {
	svc := &GatewayService{}
	ctx := context.Background()
	model := "claude-opus-4-6"
	now := time.Now().UTC()

	direct := Account{
		ID:          397,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		LastUsedAt:  &now,
	}

	creditsFallback := Account{
		ID:          425,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		Extra: map[string]any{
			"allow_overages": true,
		},
	}
	creditsFallback.Extra[buildAntigravityCreditsOveragesExtraKey(resolveCreditsOveragesModelKey(ctx, &creditsFallback, "", model))] = map[string]any{
		"active_until": now.Add(30 * time.Minute).Format(time.RFC3339),
	}

	modelLimited := Account{
		ID:          421,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				resolveFinalAntigravityModelKey(ctx, &direct, model): map[string]any{
					"rate_limit_reset_at": now.Add(30 * time.Minute).Format(time.RFC3339),
				},
			},
		},
	}

	excluded := Account{
		ID:          429,
		Platform:    PlatformAntigravity,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    1,
	}

	items := svc.buildAntigravitySelectionTraceCandidates(
		ctx,
		[]Account{direct, creditsFallback, modelLimited, excluded},
		model,
		map[int64]struct{}{429: {}},
	)

	if len(items) != 4 {
		t.Fatalf("len(items)=%d want=4", len(items))
	}

	byID := make(map[int64]antigravitySelectionCandidateTrace, len(items))
	for _, item := range items {
		byID[item.AccountID] = item
	}

	if byID[397].Mode != "direct" || byID[397].Status != "eligible" {
		t.Fatalf("direct trace=%+v want mode=direct status=eligible", byID[397])
	}
	if byID[425].Mode != "credits_fallback" || byID[425].Status != "eligible" {
		t.Fatalf("fallback trace=%+v want mode=credits_fallback status=eligible", byID[425])
	}
	if byID[421].Reason != "model_rate_limited" || byID[421].Status != "filtered" {
		t.Fatalf("model-limited trace=%+v want filtered model_rate_limited", byID[421])
	}
	if byID[429].Reason != "excluded" || byID[429].Status != "filtered" {
		t.Fatalf("excluded trace=%+v want filtered excluded", byID[429])
	}
}

func TestFilterByMinAntigravityRuntimePenalty_PrefersRecentHealthyAccount(t *testing.T) {
	svc := &GatewayService{}
	ctx := context.WithValue(context.Background(), antigravityRecentStatsPrefetchContextKey, map[int64]*RecentSuccessStats{
		101: {
			RecentSuccessCount: 1,
			RecentRequestCount: 10,
		},
		202: {
			RecentSuccessCount: 9,
			RecentRequestCount: 10,
		},
	})

	filtered := svc.filterByMinAntigravityRuntimePenalty(ctx, []accountWithLoad{
		{
			account: &Account{
				ID:       101,
				Platform: PlatformAntigravity,
			},
		},
		{
			account: &Account{
				ID:       202,
				Platform: PlatformAntigravity,
			},
		},
	})

	if len(filtered) != 1 {
		t.Fatalf("len(filtered)=%d want=1", len(filtered))
	}
	if filtered[0].account == nil || filtered[0].account.ID != 202 {
		t.Fatalf("selected account=%v want=202", filtered[0].account)
	}
}
func TestAnnotateAntigravitySelectionTraceCandidates(t *testing.T) {
	items := []antigravitySelectionCandidateTrace{
		{AccountID: 397, Mode: "direct", Status: "eligible"},
		{AccountID: 425, Mode: "credits_fallback", Status: "eligible"},
		{AccountID: 429, Mode: "credits_fallback", Status: "eligible"},
	}

	available := []accountWithLoad{
		{
			account: &Account{ID: 397},
			loadInfo: &AccountLoadInfo{
				AccountID:          397,
				CurrentConcurrency: 0,
				WaitingCount:       0,
				LoadRate:           5,
			},
		},
		{
			account: &Account{ID: 425},
			loadInfo: &AccountLoadInfo{
				AccountID:          425,
				CurrentConcurrency: 0,
				WaitingCount:       0,
				LoadRate:           7,
			},
		},
	}
	waitCandidates := []accountWithLoad{
		{
			account: &Account{ID: 429},
			loadInfo: &AccountLoadInfo{
				AccountID:          429,
				CurrentConcurrency: 1,
				WaitingCount:       2,
				LoadRate:           100,
			},
		},
	}

	annotated := annotateAntigravitySelectionTraceCandidates(items, available, waitCandidates, 397, "immediate", []int64{397, 425})
	byID := make(map[int64]antigravitySelectionCandidateTrace, len(annotated))
	for _, item := range annotated {
		byID[item.AccountID] = item
	}

	if byID[397].Status != "selected" || byID[397].LoadRate != 5 {
		t.Fatalf("selected trace=%+v want status=selected load_rate=5", byID[397])
	}
	if byID[425].Status != "immediate_candidate" || byID[425].LoadRate != 7 {
		t.Fatalf("immediate trace=%+v want status=immediate_candidate load_rate=7", byID[425])
	}
	if byID[429].Status != "wait_candidate" || byID[429].WaitingCount != 2 {
		t.Fatalf("wait trace=%+v want status=wait_candidate waiting_count=2", byID[429])
	}
}
