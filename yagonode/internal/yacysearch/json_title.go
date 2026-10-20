package yacysearch

import (
	"encoding/json"
	"strings"
)

func yacyDisplayTitle(title string) string {
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

func yacyDisplayDescription(title, description string) string {
	displayDescription := yacyDisplayTitle(description)
	if displayDescription != description && displayDescription == yacyDisplayTitle(title) {
		return displayDescription
	}
	return description
}
