package main

import (
	"maps"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
)

func TestUserLabels(t *testing.T) {
	users := map[string]map[string]string{
		"default": {
			"app": "app-prod-.*",
		},
		"user1@corp.domain": {
			"namespace": "namespace1",
		},
		"user2@corp.domain": {
			"app": "app-stage-.*",
		},
		"admin": {},
	}

	cases := []struct {
		desc string
		user string
		in   map[string]map[string]string
		out  map[string]string
		ok   bool
	}{
		{
			desc: "unknown user gets default labels",
			user: "unknown@corp.domain",
			in:   users,
			out: map[string]string{
				"app": "app-prod-.*",
			},
			ok: true,
		},
		{
			desc: "default user gets default labels",
			user: "default",
			in:   users,
			out: map[string]string{
				"app": "app-prod-.*",
			},
			ok: true,
		},
		{
			desc: "empty default makes unknown user unrestricted",
			user: "unknown@corp.domain",
			in: map[string]map[string]string{
				"default": {},
				"user1@corp.domain": {
					"namespace": "namespace1",
				},
			},
			out: map[string]string{},
			ok:  true,
		},
		{
			desc: "empty default keeps known user labels",
			user: "user1@corp.domain",
			in: map[string]map[string]string{
				"default": {},
				"user1@corp.domain": {
					"namespace": "namespace1",
				},
			},
			out: map[string]string{
				"namespace": "namespace1",
			},
			ok: true,
		},
		{
			desc: "known user labels merge with defaults",
			user: "user1@corp.domain",
			in:   users,
			out: map[string]string{
				"app":       "app-prod-.*",
				"namespace": "namespace1",
			},
			ok: true,
		},
		{
			desc: "same label values merge as regexp or",
			user: "user2@corp.domain",
			in:   users,
			out: map[string]string{
				"app": "app-prod-.*|app-stage-.*",
			},
			ok: true,
		},
		{
			desc: "empty user labels are unrestricted",
			user: "admin",
			in:   users,
			out:  map[string]string{},
			ok:   true,
		},
		{
			desc: "unknown user without default is not found",
			user: "unknown@corp.domain",
			in: map[string]map[string]string{
				"user1@corp.domain": {
					"namespace": "namespace1",
				},
			},
			out: nil,
			ok:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			res, ok := userLabels(c.in, c.user)
			if ok != c.ok {
				t.Errorf("expected ok %t, got %t", c.ok, ok)
			}
			if !maps.Equal(res, c.out) {
				t.Errorf("expected:\n %v\ngot:\n %v", c.out, res)
			}
		})
	}
}

func TestMergeLabels(t *testing.T) {
	defaults := map[string]string{
		"app":     "app-prod-.*",
		"cluster": "cluster1",
	}
	user := map[string]string{
		"app":       "app-stage-.*",
		"namespace": "namespace1",
	}

	res := mergeLabels(defaults, user)
	expected := map[string]string{
		"app":       "app-prod-.*|app-stage-.*",
		"cluster":   "cluster1",
		"namespace": "namespace1",
	}
	if !maps.Equal(res, expected) {
		t.Errorf("expected:\n %v\ngot:\n %v", expected, res)
	}

	if !maps.Equal(defaults, map[string]string{"app": "app-prod-.*", "cluster": "cluster1"}) {
		t.Errorf("defaults were mutated: %v", defaults)
	}
	if !maps.Equal(user, map[string]string{"app": "app-stage-.*", "namespace": "namespace1"}) {
		t.Errorf("user labels were mutated: %v", user)
	}
}

func TestLogqlLabels(t *testing.T) {
	cases := []struct {
		desc   string
		input  string
		assign labels.Labels
		out    string
		error  bool
	}{
		{
			desc:   "single",
			input:  `{app="test1"} | json`,
			assign: labels.Labels{{Name: "index", Value: "index1"}},
			out:    `{app="test1", index=~"index1"} | json`,
			error:  false,
		},
		{
			desc:   "double",
			input:  `rate({app="test1"} | json [1m]) + rate({app="test2"} | json [1m])`,
			assign: labels.Labels{{Name: "index", Value: "index1"}},
			out:    `(rate({app="test1", index=~"index1"} | json[1m]) + rate({app="test2", index=~"index1"} | json[1m]))`,
			error:  false,
		},
		{
			desc:   "no matchers",
			input:  `json`,
			assign: labels.Labels{{Name: "index", Value: "index1"}},
			out:    "",
			error:  true,
		},
		{
			desc:   "empty matchers",
			input:  `{} | json`,
			assign: labels.Labels{{Name: "index", Value: "index1"}},
			out:    "",
			error:  true,
		},
		{
			// https://grafana.com/docs/loki/latest/query/#pattern-match-filter-operators
			desc:   "syntax v3",
			input:  `approx_topk(10, count_over_time({app="app1"} |> "<_> caller=http.go:194 <_>"[1m]))`,
			assign: labels.Labels{{Name: "index", Value: "index1"}},
			out:    `approx_topk(10,count_over_time({app="app1", index=~"index1"} |> "<_> caller=http.go:194 <_>"[1m]))`,
			error:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			res, err := logqlLabels(c.input, c.assign)
			if c.error {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if res != c.out {
				t.Errorf("expected:\n %s\ngot:\n %s", c.out, res)
			}
		})
	}

}
