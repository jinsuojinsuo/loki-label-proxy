package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/loki/v3/pkg/logql/syntax"
	"github.com/prometheus/prometheus/model/labels"
)

func TestCodexProxyUpstreamPathKeepsAccessAndParameters(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
	}{
		{"root upstream", "", "/loki/api/v1/labels"},
		{"prefix with trailing slash", "/l1k1i1/", "/l1k1i1/loki/api/v1/labels"},
		{"prefix without trailing slash", "/l1k1i1", "/l1k1i1/loki/api/v1/labels"},
		{"escaped prefix remains escaped", "/tenant%2Fblue/", "/tenant%2Fblue/loki/api/v1/labels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type observedRequest struct {
				path, selector, start, end, user, password string
			}
			seen := make(chan observedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, _ := r.BasicAuth()
				seen <- observedRequest{
					r.URL.EscapedPath(), r.URL.Query().Get("query"),
					r.URL.Query().Get("start"), r.URL.Query().Get("end"),
					user, password,
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()

			addr := upstream.URL + tc.base
			empty, upstreamUser, upstreamPassword := "", "upstream-user", "upstream-password"
			cfg := &Config{orgs: map[string]OrgConfig{
				"fake": {Org: "fake", Users: map[string]map[string]string{
					"default": {"job": "a^"},
					"test":    {"job": "test-.+"},
				}},
			}}
			enforcer := newEnforcer(&addr, &upstreamUser, &upstreamPassword, &empty, &empty, cfg, log.NewNopLogger())
			params := url.Values{"query": {`{hostname="sgp-a"}`}, "start": {"100"}, "end": {"200"}}
			req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/labels?"+params.Encode(), nil)
			req.Header.Set("X-Scope-OrgID", "fake")
			req.Header.Set("X-Grafana-User", "test")
			rec := httptest.NewRecorder()
			newHTTPHandler(enforcer).ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("upstream response: status=%d body=%s", rec.Code, rec.Body.String())
			}

			got := <-seen
			if got.path != tc.path {
				t.Fatalf("upstream path=%q, want %q", got.path, tc.path)
			}
			if got.start != "100" || got.end != "200" {
				t.Fatalf("time range lost: start=%q end=%q", got.start, got.end)
			}
			if got.user != "upstream-user" || got.password != "upstream-password" {
				t.Fatal("upstream Basic Auth credentials were not preserved")
			}
			matchers, err := syntax.ParseMatchers(got.selector, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(matchers) != 2 {
				t.Fatalf("original filter or access constraint lost: %s", got.selector)
			}
			for _, m := range matchers {
				switch m.Name {
				case "hostname":
					if m.Type != labels.MatchEqual || m.Value != "sgp-a" {
						t.Fatalf("original hostname filter changed: %s", got.selector)
					}
				case "job":
					if m.Type != labels.MatchRegexp || m.Value != "a^|test-.+" {
						t.Fatalf("user access constraint changed: %s", got.selector)
					}
				default:
					t.Fatalf("unexpected matcher: %s", got.selector)
				}
			}
		})
	}
}
