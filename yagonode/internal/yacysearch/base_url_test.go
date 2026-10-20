package yacysearch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredBaseURLOverridesRequestBase(t *testing.T) {
	SetBaseURLProvider(func() string { return "https://peers.example.org" })
	t.Cleanup(func() { SetBaseURLProvider(func() string { return "" }) })

	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/yacysearch.json?query=x",
		nil,
	)
	if got := requestBaseURL(req); got != "https://peers.example.org" {
		t.Fatalf("base = %q", got)
	}

	SetBaseURLProvider(func() string { return "" })
	if got := requestBaseURL(req); got != "http://"+req.Host {
		t.Fatalf("fallback base = %q", got)
	}
	SetBaseURLProvider(nil)
	baseURLProvider.Store(nil)
	if got := configuredBaseURL(); got != "" {
		t.Fatalf("unset provider = %q", got)
	}
	SetBaseURLProvider(func() string { return "" })
}

func TestRequestBaseURLHonorsForwardedProto(t *testing.T) {
	SetBaseURLProvider(func() string { return "" })
	t.Cleanup(func() { baseURLProvider.Store(nil) })

	for _, test := range []struct {
		forwardedProto string
		want           string
	}{
		{forwardedProto: "https", want: "https://node.test"},
		{forwardedProto: "http", want: "http://node.test"},
		{want: "http://node.test"},
	} {
		req := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://node.test/yacysearch.html",
			nil,
		)
		if test.forwardedProto != "" {
			req.Header.Set("X-Forwarded-Proto", test.forwardedProto)
		}
		if got := requestBaseURL(req); got != test.want {
			t.Errorf("forwarded proto %q: base = %q, want %q", test.forwardedProto, got, test.want)
		}
	}
}
