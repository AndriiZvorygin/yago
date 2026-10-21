package yacysearch

import (
	_ "embed"
	"html/template"
)

//go:embed html_search_theme.css
var htmlSearchTheme string

var htmlSearchThemeCSS = template.CSS(htmlSearchTheme)
