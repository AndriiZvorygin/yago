package yacysearch

import (
	"encoding/json"
	"strings"
)

func yacyJSONDisplayTitle(title string) string {
	var values []string
	if err := json.Unmarshal([]byte(title), &values); err != nil {
		return title
	}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return title
}

func yacyJSONDisplayDescription(title, description string) string {
	displayDescription := yacyJSONDisplayTitle(description)
	if displayDescription != description && displayDescription == yacyJSONDisplayTitle(title) {
		return displayDescription
	}
	return description
}
