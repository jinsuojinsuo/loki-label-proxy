package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/loki/v3/pkg/logql/syntax"
	"github.com/prometheus/prometheus/model/labels"
)

type Enforcer struct {
	logger      log.Logger
	lokiUser    *string
	lokiPass    *string
	authUser    *string
	authPassSha *string
	target      *url.URL
	config      *Config
	proxyPass   *httputil.ReverseProxy
	proxyLabels *httputil.ReverseProxy
	proxyQuery  *httputil.ReverseProxy
	proxySeries *httputil.ReverseProxy
}

func newEnforcer(addr *string, lokiUser *string, lokiPass *string, authUser *string, authPassSha *string, cfg *Config, logger log.Logger) *Enforcer {
	target, err := url.Parse(*addr)
	if err != nil {
		level.Error(logger).Log("msg", "Unable to parse addr as url", "error", err)
		os.Exit(1)
	}
	level.Info(logger).Log("msg", fmt.Sprintf("Listening on :8080, forwarding to Loki upstream: %s://%s%s", target.Scheme, target.Host, target.EscapedPath()))

	e := &Enforcer{
		target:      target,
		lokiUser:    lokiUser,
		lokiPass:    lokiPass,
		authUser:    authUser,
		authPassSha: authPassSha,
		logger:      logger,
		config:      cfg,
	}
	e.proxyPass = e.proxyFactory("")
	e.proxyLabels = e.proxyFactory("labels")
	e.proxyQuery = e.proxyFactory("query")
	e.proxySeries = e.proxyFactory("match[]")
	return e
}

func (e *Enforcer) NotFound(w http.ResponseWriter, r *http.Request) {
	level.Debug(e.logger).Log("msg", "NotFound", "request", dumpReq(r, false))
	if e.basicAuth(w, r) {
		w.WriteHeader(http.StatusNotFound)
	}
}

func (e *Enforcer) Health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (e *Enforcer) Pass(w http.ResponseWriter, r *http.Request) {
	if e.basicAuth(w, r) {
		e.proxyPass.ServeHTTP(w, r)
	}
}

func (e *Enforcer) Labels(w http.ResponseWriter, r *http.Request) {
	if e.basicAuth(w, r) {
		e.proxyLabels.ServeHTTP(w, r)
	}
}

func (e *Enforcer) Query(w http.ResponseWriter, r *http.Request) {
	if e.basicAuth(w, r) {
		e.proxyQuery.ServeHTTP(w, r)
	}
}

func (e *Enforcer) Series(w http.ResponseWriter, r *http.Request) {
	if e.basicAuth(w, r) {
		e.proxySeries.ServeHTTP(w, r)
	}
}

func (e *Enforcer) basicAuth(w http.ResponseWriter, r *http.Request) bool {
	if *e.authUser == "" {
		return true
	}

	username, password, ok := r.BasicAuth()
	if ok {
		if username == *e.authUser && *e.authPassSha == fmt.Sprintf("%x", sha256.Sum256([]byte(password))) {
			return true
		}
		level.Info(e.logger).Log("msg", "Auth failed", "request", dumpReq(r, false), "user", username, "ip", r.RemoteAddr, "forwarded-for", r.Header.Get("X-Forwarded-For"))
		time.Sleep(3 * time.Second)
	}
	w.Header().Set("WWW-Authenticate", `Invalid token!`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}

// Routing errors stay in request context so hop-by-hop headers cannot remove them.
type routingErrorKey struct{}

type roundTripperFilter struct {
	parent http.RoundTripper
}

func (rtf *roundTripperFilter) RoundTrip(r *http.Request) (*http.Response, error) {
	if err, ok := r.Context().Value(routingErrorKey{}).(error); ok {
		return nil, err
	}
	return rtf.parent.RoundTrip(r)
}

func (e *Enforcer) proxyFactory(enforce string) *httputil.ReverseProxy {
	rewriteURL := httputil.NewSingleHostReverseProxy(e.target).Director
	director := func(req *http.Request) {
		rewriteURL(req)
		req.Host = e.target.Host

		assign, err := e.lookupUser(req)
		if err != nil {
			level.Info(e.logger).Log("msg", "Request denied", "request", dumpReq(req, false), "error", err, "ip", req.RemoteAddr, "forwarded-for", req.Header.Get("X-Forwarded-For"))
			*req = *req.WithContext(context.WithValue(req.Context(), routingErrorKey{}, err))
			return
		}
		if err := rewriteReq(enforce, req, assign, e.logger); err != nil {
			*req = *req.WithContext(context.WithValue(req.Context(), routingErrorKey{}, err))
			level.Info(e.logger).Log("msg", "Unable to rewrite request", "request", dumpReq(req, false), "error", err, "ip", req.RemoteAddr, "forwarded-for", req.Header.Get("X-Forwarded-For"))
			return
		}

		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
		level.Debug(e.logger).Log("enforce", enforce, "request", dumpReq(req, true))
		if e.lokiUser != nil && e.lokiPass != nil {
			req.SetBasicAuth(*e.lokiUser, *e.lokiPass)
		}
	}
	return &httputil.ReverseProxy{
		Director:  director,
		Transport: &roundTripperFilter{parent: http.DefaultTransport},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if routingErr, ok := req.Context().Value(routingErrorKey{}).(error); ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(routingErr.Error() + "\n"))
				return
			}
		},
	}
}

func (e *Enforcer) lookupUser(req *http.Request) (labels.Labels, error) {
	org := req.Header.Get("X-Scope-OrgID")
	if org == "" {
		return nil, fmt.Errorf("X-Scope-OrgID header not found in request")
	}
	user := strings.ToLower(req.Header.Get("X-Grafana-User"))
	if user == "" {
		return nil, fmt.Errorf("X-Grafana-User header not found in request")
	}
	if _, ok := e.config.orgs[org]; !ok {
		return nil, fmt.Errorf("X-Scope-OrgID %s not found in configs", org)
	}
	m, ok := userLabels(e.config.orgs[org].Users, user)
	if !ok {
		return nil, fmt.Errorf("no user %s found in config, and no `default` defined", user)
	}
	return labels.FromMap(m), nil
}

func userLabels(users map[string]map[string]string, user string) (map[string]string, bool) {
	defaults, hasDefault := users["default"]
	if u, ok := users[user]; ok {
		if user == "default" {
			return defaults, true
		}
		if len(u) == 0 {
			return map[string]string{}, true
		}
		return mergeLabels(defaults, u), true
	}
	if hasDefault {
		return defaults, true
	}
	return nil, false
}

func mergeLabels(defaults map[string]string, user map[string]string) map[string]string {
	merged := make(map[string]string, len(defaults)+len(user))
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range user {
		if defaultValue, ok := merged[k]; ok && defaultValue != v {
			merged[k] = defaultValue + "|" + v
			continue
		}
		merged[k] = v
	}
	return merged
}

// Parse LogQL and add labels to series selector.
// It is parseError if query does not have series selector.
func logqlLabels(logql string, assign labels.Labels) (string, error) {
	logql = strings.TrimSpace(logql)

	parsed, err := syntax.ParseExpr(logql)
	if err != nil {
		return "", err
	}
	parsed.Walk(func(x syntax.Expr) {
		switch me := x.(type) {
		case *syntax.MatchersExpr:
			for _, l := range assign {
				me.AppendMatchers([]*labels.Matcher{{
					Name:  l.Name,
					Type:  labels.MatchRegexp,
					Value: l.Value,
				}})
			}
		default:
			// do nothing
		}
	})
	return parsed.String(), nil
}

// dumpReq pretty prints the request for logging
func dumpReq(req *http.Request, debug bool) string {
	if debug {
		dump, _ := httputil.DumpRequestOut(req, true)
		return string(dump)
	}

	d := ""
	if len(req.URL.RawQuery) > 0 {
		d = "?"
	}
	qs, err := url.QueryUnescape(req.URL.RawQuery)
	if err != nil {
		qs = req.URL.RawQuery
	}
	// to dump POST need to read the body
	return fmt.Sprintf("%s %s%s%s", req.Method, req.URL.Path, d, qs)
}

// rewriteReq set/add the field enforce in GET/POST request
func rewriteReq(enforce string, req *http.Request, assign labels.Labels, logger log.Logger) error {
	if enforce == "labels" {
		return rewriteLabelsReq(req, assign)
	}
	if enforce != "" {
		if req.Method == "GET" {
			qs := req.URL.Query()
			if err := rewriteField(enforce, req, &qs, assign, logger); err != nil {
				return err
			}
			req.URL.RawQuery = qs.Encode()
		} else if req.Method == "POST" {
			if err := req.ParseForm(); err != nil {
				return err
			}
			if err := rewriteField(enforce, req, &req.Form, assign, logger); err != nil {
				return err
			}
			data := req.Form.Encode()
			req.Body = io.NopCloser(strings.NewReader(data))
			req.ContentLength = int64(len(data))
		} else {
			level.Error(logger).Log("msg", "unsupported method", "method", req.Method)
			return fmt.Errorf("unsupported method %s", req.Method)
		}
	}
	return nil
}

// rewriteLabelsReq constrains metadata to streams visible to the current user.
func rewriteLabelsReq(req *http.Request, assign labels.Labels) error {
	if req.Method != http.MethodGet {
		return fmt.Errorf("unsupported metadata method %s", req.Method)
	}
	qs, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return err
	}
	if len(qs["query"]) > 1 {
		return errors.New("only one metadata query is allowed")
	}
	var matchers []*labels.Matcher
	if selector := strings.TrimSpace(qs.Get("query")); selector != "" {
		matchers, err = syntax.ParseMatchers(selector, false)
		if err != nil {
			return err
		}
	}
	if len(assign) == 0 {
		return nil
	}
	for _, label := range assign {
		matcher, err := labels.NewMatcher(labels.MatchRegexp, label.Name, label.Value)
		if err != nil {
			return err
		}
		matchers = append(matchers, matcher)
	}
	qs.Set("query", syntax.MatchersString(matchers))
	req.URL.RawQuery = qs.Encode()
	return nil
}

// rewriteField rewrites field in the query values, or adds if it is empty/not-set
func rewriteField(field string, req *http.Request, form *url.Values, assign labels.Labels, logger log.Logger) error {
	if _, ok := (*form)[field]; ok {
		for i, m := range (*form)[field] {
			if q, err := logqlLabels(m, assign); err == nil {
				(*form)[field][i] = q
			} else {
				level.Error(logger).Log("msg", "failed to parse", field, m, "error", err)
				return err
			}
		}
	} else {
		level.Error(logger).Log("msg", fmt.Sprintf("%s is empty, assigning labels", field))
		x := syntax.MatchersExpr{}
		for _, l := range assign {
			x.AppendMatchers([]*labels.Matcher{{
				Name:  l.Name,
				Type:  labels.MatchRegexp,
				Value: l.Value,
			}})
		}
		(*form)[field] = []string{x.String()}
	}

	return nil
}
