package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/loki/v3/pkg/logql/syntax"
)

func TestCodexMetadataListsRespectAccess(t *testing.T) {
	handler, _ := codexMetadataFixture(t)
	cases := []struct {
		path string
		want []string
	}{
		{"/loki/api/v1/label/job/values", []string{"prod-singapore-luck77"}},
		{"/loki/api/v1/label/hostname/values", []string{"sgp-a", "sgp-c"}},
		{"/loki/api/v1/label/service_name/values", []string{"luck77"}},
		{"/loki/api/v1/labels", []string{"hostname", "job", "service_name"}},
		{"/loki/api/v1/label", []string{"hostname", "job", "service_name"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := codexMetadataRequest(t, handler, tc.path, "alice", http.StatusOK)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unauthorized metadata: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexMetadataFiltersIntersectWithAccess(t *testing.T) {
	handler, _ := codexMetadataFixture(t)
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"existing filter retained", `{hostname="sgp-c"}`, []string{"sgp-c"}},
		{"conflicting job cannot widen access", `{job="prod-frank-lucky_gift"}`, []string{}},
		{"empty optional filter", "", []string{"sgp-a", "sgp-c"}},
		{"empty selector", "{}", []string{"sgp-a", "sgp-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := url.Values{"query": {tc.query}, "start": {"100"}, "end": {"200"}}
			got := codexMetadataRequest(t, handler, "/loki/api/v1/label/hostname/values?"+params.Encode(), "alice", http.StatusOK)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("filter or access boundary lost: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexMetadataInvalidRequestsNeverReachLoki(t *testing.T) {
	handler, calls := codexMetadataFixture(t)
	cases := []struct {
		name string
		path string
		user string
	}{
		{"missing identity", "/loki/api/v1/label/job/values", ""},
		{"malformed selector", "/loki/api/v1/label/job/values?" + url.Values{"query": {`{job=`}}.Encode(), "alice"},
		{"admin malformed selector", "/loki/api/v1/label/job/values?" + url.Values{"query": {`{job=`}}.Encode(), "admin"},
		{"duplicate query parameters", "/loki/api/v1/label/job/values?" + url.Values{"query": {`{job=~".+"}`, `{job="prod-singapore-luck77"}`}}.Encode(), "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			codexMetadataRequest(t, handler, tc.path, tc.user, http.StatusBadRequest)
			if calls.Load() != before {
				t.Fatal("rejected request reached Loki")
			}
		})
	}
}

func TestCodexMetadataRejectionCannotBeStripped(t *testing.T) {
	handler, calls := codexMetadataFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/label/job/values?query=&query=&start=100&end=200", nil)
	req.Header.Set("X-Scope-OrgID", "fake")
	req.Header.Set("X-Grafana-User", "alice")
	req.Header.Set("Connection", "X-Routing-Error")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("request bypassed rejection: status=%d upstream_calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
	}
}

func TestCodexMetadataUnrestrictedUserKeepsAccess(t *testing.T) {
	handler, _ := codexMetadataFixture(t)
	want := []string{"prod-frank-lucky_gift", "prod-singapore-luck77"}
	got := codexMetadataRequest(t, handler, "/loki/api/v1/label/job/values", "admin", http.StatusOK)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unrestricted user lost access: got %v, want %v", got, want)
	}
	got = codexMetadataRequest(t, handler, "/loki/api/v1/label/job/values?"+url.Values{"query": {`{service_name="lucky_gift"}`}}.Encode(), "admin", http.StatusOK)
	if !reflect.DeepEqual(got, []string{"prod-frank-lucky_gift"}) {
		t.Fatalf("unrestricted user's filter lost: got %v", got)
	}
}

func codexMetadataFixture(t *testing.T) (http.Handler, *atomic.Int32) {
	t.Helper()
	streams := []map[string]string{
		{"job": "prod-singapore-luck77", "hostname": "sgp-a", "service_name": "luck77"},
		{"job": "prod-frank-lucky_gift", "hostname": "frank-b", "service_name": "lucky_gift", "private_label": "secret"},
		{"job": "prod-singapore-luck77", "hostname": "sgp-c", "service_name": "luck77"},
	}
	calls := new(atomic.Int32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		params := r.URL.Query()
		if params.Get("start") != "100" || params.Get("end") != "200" {
			http.Error(w, "time range was changed", http.StatusBadRequest)
			return
		}
		selector := params.Get("query")
		if selector == "" {
			selector = "{}"
		}
		matchers, err := syntax.ParseMatchers(selector, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		values := map[string]bool{}
		for _, stream := range streams {
			matches := true
			for _, matcher := range matchers {
				if !matcher.Matches(stream[matcher.Name]) {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			if strings.HasSuffix(r.URL.Path, "/values") {
				label := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/loki/api/v1/label/"), "/values")
				if value, ok := stream[label]; ok {
					values[value] = true
				}
			} else {
				for label := range stream {
					values[label] = true
				}
			}
		}
		result := make([]string, 0, len(values))
		for value := range values {
			result = append(result, value)
		}
		sort.Strings(result)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Status string   `json:"status"`
			Data   []string `json:"data"`
		}{"success", result})
	}))
	t.Cleanup(upstream.Close)
	empty := ""
	addr := upstream.URL
	cfg := &Config{orgs: map[string]OrgConfig{
		"fake": {Org: "fake", Users: map[string]map[string]string{
			"default": {"job": "a^"},
			"alice":   {"job": "prod-singapore-luck77"},
			"admin":   {},
		}},
	}}
	enforcer := newEnforcer(&addr, &empty, &empty, &empty, &empty, cfg, log.NewNopLogger())
	return newHTTPHandler(enforcer), calls
}

func codexMetadataRequest(t *testing.T, handler http.Handler, path, user string, wantStatus int) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	params := req.URL.Query()
	params.Set("start", "100")
	params.Set("end", "200")
	req.URL.RawQuery = params.Encode()
	req.Header.Set("X-Scope-OrgID", "fake")
	if user != "" {
		req.Header.Set("X-Grafana-User", user)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status %d, want %d; body: %s", rec.Code, wantStatus, rec.Body.String())
	}
	if wantStatus != http.StatusOK {
		return nil
	}
	var response struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}
