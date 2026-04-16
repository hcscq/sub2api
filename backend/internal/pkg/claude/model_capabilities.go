package claude

import (
	"regexp"
	"strconv"
	"strings"
)

var claudeFamilyVersionRe = regexp.MustCompile(`claude-(haiku|sonnet|opus)-(\d+)[-.](\d{1,2})(?:$|[^0-9])`)

// ParseFamilyVersion extracts the Claude family and major/minor version from a
// model ID. It supports raw Anthropic IDs and Bedrock IDs containing a Claude
// model segment.
func ParseFamilyVersion(modelID string) (family string, major int, minor int, ok bool) {
	matches := claudeFamilyVersionRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(modelID)))
	if matches == nil {
		return "", 0, 0, false
	}

	major, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", 0, 0, false
	}
	minor, err = strconv.Atoi(matches[3])
	if err != nil {
		return "", 0, 0, false
	}

	return matches[1], major, minor, true
}

// IsOpus47OrNewer reports whether the model is Claude Opus 4.7 or newer.
func IsOpus47OrNewer(modelID string) bool {
	family, major, minor, ok := ParseFamilyVersion(modelID)
	if !ok || family != "opus" {
		return false
	}
	return major > 4 || (major == 4 && minor >= 7)
}

// IsOpus46Model reports whether the model is Claude Opus 4.6.x.
func IsOpus46Model(modelID string) bool {
	family, major, minor, ok := ParseFamilyVersion(modelID)
	return ok && family == "opus" && major == 4 && minor == 6
}
