package main

import (
	"testing"

	"github.com/prometheus/prometheus/model/labels"
)

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
