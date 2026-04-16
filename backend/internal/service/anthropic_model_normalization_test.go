package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeAnthropicOpus47RequestBody_ConvertsEnabledThinkingAndStripsUnsupportedSampling(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":4096},"temperature":0.2,"top_p":0.7,"top_k":16}`)

	result, changed := normalizeAnthropicOpus47RequestBody(body, "claude-opus-4-7")
	require.True(t, changed)
	require.Equal(t, "adaptive", gjson.GetBytes(result, "thinking.type").String())
	require.False(t, gjson.GetBytes(result, "thinking.budget_tokens").Exists())
	require.False(t, gjson.GetBytes(result, "temperature").Exists())
	require.False(t, gjson.GetBytes(result, "top_p").Exists())
	require.False(t, gjson.GetBytes(result, "top_k").Exists())
}

func TestNormalizeAnthropicOpus47RequestBody_LeavesOpus46Untouched(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":4096},"temperature":0.2,"top_p":0.7,"top_k":16}`)

	result, changed := normalizeAnthropicOpus47RequestBody(body, "claude-opus-4-6")
	require.False(t, changed)
	require.JSONEq(t, string(body), string(result))
}
