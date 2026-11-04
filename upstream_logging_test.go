package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
)

type upstreamLoggingTransport func(*http.Request) (*http.Response, error)

func (f upstreamLoggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newUpstreamLoggingProxy() (*httputil.ReverseProxy, *bytes.Buffer) {
	output := &bytes.Buffer{}
	logger := level.NewFilter(log.NewJSONLogger(output), level.AllowInfo())
	addr := "https://url-user:url-secret@upstream.example/prefix/"
	empty := ""
	cfg := &Config{orgs: map[string]OrgConfig{
		"fake": {Users: map[string]map[string]string{"reader": {}}},
	}}
	enforcer := newEnforcer(&addr, &empty, &empty, &empty, &empty, cfg, logger)
	output.Reset()
	return enforcer.proxyPass, output
}

func upstreamLoggingRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/status/buildinfo?token=query-secret", nil)
	req.Header.Set("X-Scope-OrgID", "fake")
	req.Header.Set("X-Grafana-User", "reader")
	req.Header.Set("Authorization", "Bearer header-secret")
	return req
}

func readUpstreamLog(t *testing.T, output *bytes.Buffer, wantLevel string, wantStatus int) map[string]any {
	t.Helper()
	var entry map[string]any
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	if err := decoder.Decode(&entry); err != nil {
		t.Fatalf("expected an upstream log: %v; output=%s", err, output)
	}
	var extra map[string]any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("expected one log entry, got extra=%v err=%v", extra, err)
	}
	if entry["level"] != wantLevel || entry["status"] != float64(wantStatus) {
		t.Fatalf("incorrect severity/status: %v", entry)
	}
	if entry["method"] != "GET" || entry["upstream"] != "https://upstream.example" ||
		entry["path"] != "/prefix/loki/api/v1/status/buildinfo" {
		t.Fatalf("missing request context: %v", entry)
	}
	for _, secret := range []string{"url-secret", "header-secret", "query-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("upstream log contains sensitive request/response data: %s", secret)
		}
	}
	return entry
}

// 风险：成功响应漏记日志，或日志处理改变上游响应。
func TestUpstreamLoggingHTTPResponses(t *testing.T) {
	cases := []struct {
		status int
		level  string
		body   string
	}{
		{200, "info", "private-response"},
		{204, "info", ""},
		{302, "info", "private-response"},
		{400, "error", "private-response"},
		{500, "error", "private-response"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			proxy, output := newUpstreamLoggingProxy()
			proxy.Transport.(*roundTripperFilter).parent = upstreamLoggingTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status, Status: http.StatusText(tc.status),
					Header: http.Header{"X-Upstream": {"retained"}},
					Body:   io.NopCloser(strings.NewReader(tc.body)), Request: req,
				}, nil
			})
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, upstreamLoggingRequest())
			if rec.Code != tc.status || rec.Body.String() != tc.body || rec.Header().Get("X-Upstream") != "retained" {
				t.Fatalf("upstream response changed: status=%d body=%q headers=%v", rec.Code, rec.Body.String(), rec.Header())
			}
			entry := readUpstreamLog(t, output, tc.level, tc.status)
			if tc.status == 200 {
				if _, ok := entry["body"]; ok {
					t.Fatal("200 response body must not be logged")
				}
				if strings.Contains(output.String(), "private-response") {
					t.Fatal("200 response body leaked into log")
				}
			} else if entry["body"] != tc.body || entry["body_truncated"] != false {
				t.Fatalf("non-200 response body missing or incorrectly truncated: %v", entry)
			}
		})
	}
}

// Risk: transport failures silently produce a successful empty response.
func TestUpstreamLoggingTransportFailure(t *testing.T) {
	proxy, output := newUpstreamLoggingProxy()
	proxy.Transport.(*roundTripperFilter).parent = upstreamLoggingTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, upstreamLoggingRequest())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("connection failure returned %d, want 502", rec.Code)
	}
	entry := readUpstreamLog(t, output, "error", 502)
	if entry["error"] != "dial tcp: connection refused" {
		t.Fatalf("original connection error missing: %v", entry)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatal("internal transport error leaked to client")
	}
}

// Risk: logging a valid 101 upgrade as an error can disrupt live log connections.
func TestUpstreamLoggingUpgrade(t *testing.T) {
	proxy, output := newUpstreamLoggingProxy()
	req := upstreamLoggingRequest()
	proxy.Director(req)
	body := io.NopCloser(strings.NewReader("private-response"))
	response := &http.Response{StatusCode: 101, Request: req, Body: body}
	if proxy.ModifyResponse == nil {
		t.Fatal("101 upstream response has no logging observer")
	}
	if err := proxy.ModifyResponse(response); err != nil {
		t.Fatalf("upgrade rejected by logger: %v", err)
	}
	if response.StatusCode != 101 || response.Body != body {
		t.Fatal("upgrade response altered")
	}
	entry := readUpstreamLog(t, output, "info", 101)
	if _, ok := entry["body"]; ok {
		t.Fatal("101 upgrade connection data must not be logged")
	}
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "private-response" {
		t.Fatalf("upgrade data consumed by logger: body=%q err=%v", data, err)
	}
}

// Risk: local authorization failures can be mistaken for upstream outages.
func TestUpstreamLoggingLocalRejection(t *testing.T) {
	proxy, output := newUpstreamLoggingProxy()
	proxy.Transport.(*roundTripperFilter).parent = upstreamLoggingTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("locally rejected request reached upstream")
		return nil, nil
	})
	req := upstreamLoggingRequest()
	req.Header.Del("X-Grafana-User")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("local rejection returned %d, want 400", rec.Code)
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if _, ok := entry["upstream"]; ok {
		t.Fatalf("local rejection logged as an upstream failure: %v", entry)
	}
}

// 风险：日志截断位置判断错误，或截断日志时同时截断了返回给客户端的正文。
func TestUpstreamLoggingBodyLimit(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		truncated bool
	}{
		{"exact limit", strings.Repeat("a", 4096), false},
		{"above limit", strings.Repeat("a", 4096) + "b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, output := newUpstreamLoggingProxy()
			proxy.Transport.(*roundTripperFilter).parent = upstreamLoggingTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 500, Request: req, Header: make(http.Header),
					Body: io.NopCloser(iotest.OneByteReader(strings.NewReader(tc.body))),
				}, nil
			})
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, upstreamLoggingRequest())
			if rec.Code != 500 || rec.Body.String() != tc.body {
				t.Fatalf("logging truncated client response: status=%d length=%d", rec.Code, rec.Body.Len())
			}
			entry := readUpstreamLog(t, output, "error", 500)
			if entry["body"] != strings.Repeat("a", 4096) || entry["body_truncated"] != tc.truncated {
				t.Fatalf("incorrect body prefix or truncation marker: %v", entry)
			}
		})
	}
}

type upstreamLoggingBrokenBody struct {
	read   bool
	closed bool
}

func (b *upstreamLoggingBrokenBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	return copy(p, "partial"), io.ErrUnexpectedEOF
}

func (b *upstreamLoggingBrokenBody) Close() error {
	b.closed = true
	return nil
}

// 风险：为打印正文提前读取响应，吞掉原始读取错误，或未关闭原始响应流。
func TestUpstreamLoggingBodyReadFailure(t *testing.T) {
	proxy, output := newUpstreamLoggingProxy()
	req := upstreamLoggingRequest()
	proxy.Director(req)
	body := &upstreamLoggingBrokenBody{}
	response := &http.Response{StatusCode: 500, Request: req, Body: body}
	if err := proxy.ModifyResponse(response); err != nil {
		t.Fatalf("response rejected before forwarding: %v", err)
	}
	if body.read {
		t.Fatal("logging read the body before forwarding")
	}
	data, err := io.ReadAll(response.Body)
	if string(data) != "partial" || err != io.ErrUnexpectedEOF {
		t.Fatalf("body read result changed: data=%q err=%v", data, err)
	}
	if err := response.Body.Close(); err != nil || !body.closed {
		t.Fatalf("original body was not closed: closed=%v err=%v", body.closed, err)
	}
	entry := readUpstreamLog(t, output, "error", 500)
	if entry["body"] != "partial" || entry["body_truncated"] != false {
		t.Fatalf("partial response body missing: %v", entry)
	}
}
