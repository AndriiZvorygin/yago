package yacysearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHomeEndpointRendersSearchFormAndVersion(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)

	NewHomeEndpoint("v1.2.3").ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != homePageContentType {
		t.Fatalf("content type = %q, want %q", got, homePageContentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"YagoSeek — Search", "v1.2.3", `action="/yacysearch.html"`,
		`name="query"`, `name="resource"`, `value="local"`, `value="global"`,
		`href="/account/login"`, `role="search"`, `.visually-hidden{position:absolute`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home page missing %q", want)
		}
	}
}

func TestHomeEndpointOmitsUnsetVersion(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)

	NewHomeEndpoint("").ServeHTTP(recorder, request)

	if strings.Contains(recorder.Body.String(), "class=\"version\"") {
		t.Fatal("version rendered while unset")
	}
}

func TestHomeEndpointSupportsHeadAndRejectsPost(t *testing.T) {
	headRecorder := httptest.NewRecorder()
	headRequest := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/", nil)
	NewHomeEndpoint("").ServeHTTP(headRecorder, headRequest)
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", headRecorder.Code, http.StatusOK)
	}

	postRecorder := httptest.NewRecorder()
	postRequest := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	NewHomeEndpoint("").ServeHTTP(postRecorder, postRequest)
	if postRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", postRecorder.Code, http.StatusMethodNotAllowed)
	}
	if got := postRecorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
}
