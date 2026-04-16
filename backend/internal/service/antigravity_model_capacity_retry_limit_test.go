//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandleSmartRetry_503_ModelCapacityExhausted_UsesShortRetryBudget(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	account := &Account{
		ID:       30,
		Name:     "acc-30",
		Type:     AccountTypeOAuth,
		Platform: PlatformAntigravity,
	}

	respBody := []byte(`{
		"error": {
			"code": 503,
			"status": "UNAVAILABLE",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-pro-high"}, "reason": "MODEL_CAPACITY_EXHAUSTED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "39s"}
			],
			"message": "No capacity available for model gemini-3-pro-high on the server"
		}
	}`)
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(respBody)),
	}

	upstream := &mockSmartRetryUpstream{
		responses: []*http.Response{
			{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(respBody)))},
		},
		errors:     []error{nil},
		repeatLast: true,
	}

	params := antigravityRetryLoopParams{
		ctx:          ctxWithSingleAccountRetry(),
		prefix:       "[test]",
		account:      account,
		accessToken:  "token",
		action:       "generateContent",
		body:         []byte(`{"input":"test"}`),
		accountRepo:  repo,
		httpUpstream: upstream,
		handleError: func(ctx context.Context, prefix string, account *Account, statusCode int, headers http.Header, body []byte, requestedModel string, groupID int64, sessionHash string, isStickySession bool) *handleModelRateLimitResult {
			return nil
		},
	}

	svc := &AntigravityGatewayService{}
	result := svc.handleSmartRetry(params, resp, respBody, "https://ag-1.test", 0, []string{"https://ag-1.test"})

	require.NotNil(t, result)
	require.Equal(t, smartRetryActionBreakWithResp, result.action)
	require.NotNil(t, result.resp, "should return the final upstream 503 after exhausting the short retry budget")
	require.Len(t, upstream.calls, antigravityModelCapacityRetryMaxAttempts, "MODEL_CAPACITY_EXHAUSTED should only spend the bounded retry budget")
	require.Empty(t, repo.modelRateLimitCalls, "MODEL_CAPACITY_EXHAUSTED should still avoid model rate-limit writes")
}
