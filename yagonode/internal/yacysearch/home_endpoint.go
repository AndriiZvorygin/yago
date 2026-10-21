package yacysearch

import (
	_ "embed"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed home_page.html
var homePageHTML string

var homePageTemplate = template.Must(template.New("search-home").Parse(homePageHTML))

const homePageContentType = "text/html; charset=utf-8"

type homeEndpoint struct {
	version string
}

type homePageData struct {
	Version string
}

func NewHomeEndpoint(version string) http.Handler {
	return homeEndpoint{version: version}
}

func (e homeEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

		return
	}

	w.Header().Set("Content-Type", homePageContentType)
	if err := homePageTemplate.Execute(w, homePageData{Version: e.version}); err != nil {
		slog.WarnContext(r.Context(), "search home page write failed", slog.Any("error", err))
	}
}
