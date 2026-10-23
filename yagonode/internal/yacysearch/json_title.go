package yacysearch

import (
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func yacyDisplayTitle(title string) string {
	return searchcore.DisplayTitle(title)
}

func yacyDisplayDescription(title, description string) string {
	return searchcore.DisplayDescription(title, description)
}
