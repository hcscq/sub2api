package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
)

// normalizeAnthropicOpus47RequestBody applies model-specific request shaping
// required by Claude Opus 4.7+ while leaving older Opus 4.6 behavior unchanged.
func normalizeAnthropicOpus47RequestBody(body []byte, modelID string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}

	effectiveModel := strings.TrimSpace(modelID)
	if effectiveModel == "" {
		rawModel := gjson.GetBytes(body, "model")
		if rawModel.Exists() && rawModel.Type == gjson.String {
			effectiveModel = rawModel.String()
		}
	}
	if !claude.IsOpus47OrNewer(effectiveModel) {
		return body, false
	}

	out := body
	modified := false

	thinking := gjson.GetBytes(out, "thinking")
	if thinking.Exists() && thinking.IsObject() {
		thinkingType := strings.ToLower(strings.TrimSpace(thinking.Get("type").String()))
		if thinkingType == "enabled" {
			if next, ok := setJSONValueBytes(out, "thinking.type", "adaptive"); ok {
				out = next
				modified = true
			}
			thinkingType = "adaptive"
		}
		if thinkingType == "adaptive" && thinking.Get("budget_tokens").Exists() {
			if next, ok := deleteJSONPathBytes(out, "thinking.budget_tokens"); ok {
				out = next
				modified = true
			}
		}
	}

	for _, path := range []string{"temperature", "top_p", "top_k"} {
		if gjson.GetBytes(out, path).Exists() {
			if next, ok := deleteJSONPathBytes(out, path); ok {
				out = next
				modified = true
			}
		}
	}

	return out, modified
}
