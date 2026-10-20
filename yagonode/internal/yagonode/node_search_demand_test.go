package yagonode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/searchdemand"
	"github.com/D4rk4/yago/yagonode/internal/tavilyapi"
	"github.com/D4rk4/yago/yagoproto"
)

func newDemandTestRequest(method, target string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, target, nil)
}

func TestDemandSearchRequestIncludesPublicSearchSurfacesAndSkipsPaging(t *testing.T) {
	cases := []struct {
		name    string
		request *http.Request
		want    bool
	}{
		{
			name:    "portal",
			request: newDemandTestRequest(http.MethodGet, "/?q=sample"),
			want:    true,
		},
		{name: "portal without query", request: newDemandTestRequest(http.MethodGet, "/")},
		{
			name: "YaCy JSON",
			request: newDemandTestRequest(
				http.MethodGet,
				yagoproto.PathYaCySearchJSON+"?query=sample",
			),
			want: true,
		},
		{
			name: "YaCy RSS",
			request: newDemandTestRequest(
				http.MethodGet,
				yagoproto.PathYaCySearchRSS+"?query=sample",
			),
			want: true,
		},
		{
			name: "YaCy HTML",
			request: newDemandTestRequest(
				http.MethodGet,
				yagoproto.PathYaCySearchHTML+"?query=sample",
			),
			want: true,
		},
		{
			name: "YaCy later page",
			request: newDemandTestRequest(
				http.MethodGet,
				yagoproto.PathYaCySearchJSON+"?query=sample&startRecord=10",
			),
		},
		{
			name: "YaCy offset page",
			request: newDemandTestRequest(
				http.MethodGet,
				yagoproto.PathYaCySearchJSON+"?query=sample&offset=10",
			),
		},
		{
			name:    "portal later page",
			request: newDemandTestRequest(http.MethodGet, "/?q=sample&p=2"),
		},
		{
			name:    "Tavily API",
			request: newDemandTestRequest(http.MethodPost, tavilyapi.PathSearch),
			want:    true,
		},
		{name: "unrelated endpoint", request: newDemandTestRequest(http.MethodGet, "/health")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := isDemandSearchRequest(test.request); got != test.want {
				t.Fatalf("isDemandSearchRequest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSearchDemandSettingsDefaultOffAndApplyLive(t *testing.T) {
	toggles := newRuntimeToggles(nodeConfig{})
	if toggles.SearchDemandEnabled() || toggles.SearchDemandWebDiscoveryEnabled() {
		t.Fatal("search-demand settings must default off")
	}
	toggles.SetSearchDemandEnabled(true)
	toggles.SetSearchDemandWebDiscovery(true)
	if !toggles.SearchDemandEnabled() || !toggles.SearchDemandWebDiscoveryEnabled() {
		t.Fatal("search-demand settings did not apply live")
	}
}

func TestDueDemandTopicsAreBoundedAndMakeProgress(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	topics := make([]searchdemand.Topic, 0, searchDemandTopicsPerCycle+2)
	budgets := make(map[string]uint64, searchDemandTopicsPerCycle+2)
	for index := range searchDemandTopicsPerCycle + 5 {
		id := fmt.Sprintf("topic-%03d", index)
		topics = append(topics, searchdemand.Topic{ID: id})
		budgets[id] = 1
	}
	topics = append(topics,
		searchdemand.Topic{ID: "recent", LastScheduledAt: now.Add(-time.Hour)},
		searchdemand.Topic{ID: "unfunded"},
	)

	first := dueDemandTopics(topics, budgets, now)
	if len(first) != searchDemandTopicsPerCycle {
		t.Fatalf("first cycle selected %d topics, want %d", len(first), searchDemandTopicsPerCycle)
	}
	for _, topic := range first {
		if topic.LastScheduledAt.IsZero() == false {
			t.Fatalf("recent topic %q was selected before its interval", topic.ID)
		}
		if topic.ID == "unfunded" {
			t.Fatal("topic without a positive budget was selected")
		}
	}
	for index := range first {
		first[index].LastScheduledAt = now
	}
	next := dueDemandTopics(append(first, topics[searchDemandTopicsPerCycle:]...), budgets, now)
	if len(next) != 5 {
		t.Fatalf("next cycle selected %d topics, want the five remaining", len(next))
	}
	for _, topic := range next {
		if topic.ID < "topic-100" || topic.ID > "topic-104" {
			t.Fatalf("next cycle selected unexpected topic %q", topic.ID)
		}
	}
}

func TestSearchDemandIdentityMiddlewareIsPassThroughWhenDisabled(t *testing.T) {
	toggles := newRuntimeToggles(nodeConfig{})
	var identityPresent bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		_, identityPresent = demandIdentityFromContext(request.Context())
	})
	handler := withSearchDemandRequestContext(
		next, nodeConfig{}, toggles, tavilyapi.SearchAccessPolicy{BearerToken: "read-token"},
	)
	request := newDemandTestRequest(
		http.MethodGet,
		yagoproto.PathYaCySearchJSON+"?query=sample",
	)
	request.Header.Set("Authorization", "Bearer read-token")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if identityPresent {
		t.Fatal("disabled demand middleware attached a tracking identity")
	}
}

func TestDemandIdentityUsesOnlyConfiguredTrustedProxyHeaders(t *testing.T) {
	trusted, err := parseTrustedProxies("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	config := nodeConfig{TrustedProxies: trusted}
	toggles := newRuntimeToggles(config)
	toggles.SetSearchDemandEnabled(true)
	access := tavilyapi.SearchAccessPolicy{BearerToken: "read-token"}
	cases := []struct {
		name         string
		remoteAddr   string
		forwarded    string
		wantAddr     string
		wantIdentity bool
	}{
		{
			name: "untrusted forwarding header is ignored", remoteAddr: "192.0.2.7:4000",
			forwarded: "198.51.100.8", wantAddr: "192.0.2.7", wantIdentity: true,
		},
		{
			name: "trusted proxy supplies client address", remoteAddr: "10.1.2.3:4000",
			forwarded: "198.51.100.8, 10.1.2.4", wantAddr: "198.51.100.8", wantIdentity: true,
		},
		{
			name:       "invalid trusted forwarding value is not a client identity",
			remoteAddr: "10.1.2.3:4000",
			forwarded:  "not-an-ip",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var got demandRequestIdentity
			var identityPresent bool
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got, identityPresent = demandIdentityFromContext(r.Context())
			})
			handler := withSearchDemandRequestContext(next, config, toggles, access)
			request := newDemandTestRequest(
				http.MethodGet,
				yagoproto.PathYaCySearchJSON+"?query=sample",
			)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("X-Forwarded-For", test.forwarded)
			request.Header.Set("Authorization", "Bearer read-token")
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if identityPresent != test.wantIdentity || got.clientAddress != test.wantAddr ||
				(test.wantIdentity && !got.authenticated) {
				t.Fatalf("identity = %+v, want address %q and authenticated", got, test.wantAddr)
			}
		})
	}
}
