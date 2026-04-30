package capacitybench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRangeQueryParsesMatrix(t *testing.T) {
	var gotQuery, gotStart, gotEnd, gotStep string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("path = %s, want /api/v1/query_range", r.URL.Path)
		}
		q := r.URL.Query()
		gotQuery = q.Get("query")
		gotStart = q.Get("start")
		gotEnd = q.Get("end")
		gotStep = q.Get("step")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{
				"resultType":"matrix",
				"result":[{
					"metric":{"instance":"jetmon-v1","job":"node"},
					"values":[[1000.000,"1.5"],[1015.000,"2.5"]]
				}]
			}
		}`))
	}))
	defer srv.Close()

	client := &PrometheusClient{BaseURL: srv.URL, Client: srv.Client()}
	start := time.Unix(1000, 0).UTC()
	end := time.Unix(1030, 0).UTC()
	series, err := client.RangeQuery(context.Background(), "up", start, end, 15*time.Second)
	if err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
	if gotQuery != "up" {
		t.Fatalf("query = %q, want up", gotQuery)
	}
	if gotStart != "1000.000" || gotEnd != "1030.000" || gotStep != "15s" {
		t.Fatalf("time params start=%q end=%q step=%q", gotStart, gotEnd, gotStep)
	}
	if len(series) != 1 {
		t.Fatalf("series count = %d, want 1", len(series))
	}
	if got := series[0].Metric["instance"]; got != "jetmon-v1" {
		t.Fatalf("instance = %q, want jetmon-v1", got)
	}
	if got := len(series[0].Values); got != 2 {
		t.Fatalf("sample count = %d, want 2", got)
	}
	if got := series[0].Values[1].Value; got != 2.5 {
		t.Fatalf("second value = %v, want 2.5", got)
	}
}

func TestSummarizeSeries(t *testing.T) {
	q := Query{Name: "host_cpu_used", Unit: "percent"}
	s := Series{
		Metric: map[string]string{"__name__": "ignored", "instance": "jetmon-v1"},
		Values: []Sample{
			{Value: 1},
			{Value: 3},
			{Value: 5},
			{Value: 7},
			{Value: 9},
		},
	}
	got, ok := SummarizeSeries(q, s)
	if !ok {
		t.Fatal("SummarizeSeries returned ok=false")
	}
	if got.Samples != 5 || got.Min != 1 || got.Avg != 5 || got.P50 != 5 || got.P95 != 8.6 || got.Max != 9 || got.Last != 9 {
		t.Fatalf("summary = %+v", got)
	}
	if _, ok := got.Labels["__name__"]; ok {
		t.Fatalf("__name__ label should be omitted: %+v", got.Labels)
	}
}

func TestInstanceRegexQuotesNames(t *testing.T) {
	got, err := InstanceRegex([]string{"jetmon-v1", "prod.host"})
	if err != nil {
		t.Fatalf("InstanceRegex: %v", err)
	}
	if got != `jetmon-v1|prod\.host` {
		t.Fatalf("regex = %q", got)
	}
}

func TestDefaultQueriesUseInstanceMatcher(t *testing.T) {
	queries := DefaultQueries(`jetmon-v1|jetmon-v2`, 2*time.Minute)
	if len(queries) == 0 {
		t.Fatal("DefaultQueries returned no queries")
	}
	for _, q := range queries {
		if q.Name == "" || q.Unit == "" || q.Expr == "" {
			t.Fatalf("incomplete query: %+v", q)
		}
		if !strings.Contains(q.Expr, `instance=~"jetmon-v1|jetmon-v2"`) {
			t.Fatalf("query %s missing instance matcher: %s", q.Name, q.Expr)
		}
	}
}
