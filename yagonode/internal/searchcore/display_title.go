package searchcore

import (
	"encoding/json"
	"strings"
)

func DisplayTitle(title string) string {
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

func DisplayDescription(title, description string) string {
	displayDescription := DisplayTitle(description)
	if displayDescription != description && displayDescription == DisplayTitle(title) {
		return displayDescription
	}
	return description
}
