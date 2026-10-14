package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestAllocations(t *testing.T) {
	out := []byte(`goos: linux
pkg: axiom/internal/protocol
BenchmarkScan-8            	  120000	      9876 ns/op	     512 B/op	       3 allocs/op
BenchmarkScan-8            	  118000	     10021 ns/op	     512 B/op	       4 allocs/op
BenchmarkScan/large-8      	    1000	   1234567 ns/op	    1000 allocs/op_total	   65536 B/op	      12 allocs/op
pkg: axiom/internal/routing
BenchmarkPick/case-1       	 5000000	       210 ns/op	       0 B/op	     100 allocs/op
BenchmarkPick/case-2       	 5000000	       210 ns/op	       0 B/op	       0 allocs/op
--- SKIP: BenchmarkSlow
    slow_test.go:9: needs a database
PASS
ok  	axiom/internal/routing	3.1s
`)
	samples, skips, err := allocations(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]float64{
		"axiom/internal/protocol.Scan-8":       {3, 4},
		"axiom/internal/protocol.Scan/large-8": {12}, // a custom metric before allocs/op is not mistaken for it
		"axiom/internal/routing.Pick/case-1":   {100},
		"axiom/internal/routing.Pick/case-2":   {0}, // without a GOMAXPROCS suffix, sub-benchmarks stay apart
	}
	if diff := cmp.Diff(want, samples); diff != "" {
		t.Errorf("samples (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"BenchmarkSlow"}, skips); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		name              string
		base, head        map[string][]float64
		skipped           []string
		problems, removed []string
	}{
		{"equal", map[string][]float64{"a": {3, 3}}, map[string][]float64{"a": {3, 3}}, nil, nil, nil},
		{"fewer", map[string][]float64{"a": {3}}, map[string][]float64{"a": {2}}, nil, nil, nil},
		{
			"more, by median",
			map[string][]float64{"a": {3, 3, 9}},
			map[string][]float64{"a": {4, 4, 1}},
			nil,
			[]string{"more allocations: a: 3 -> 4 allocs/op"},
			nil,
		},
		{"new benchmarks have nothing to compare", nil, map[string][]float64{"b": {9}}, nil, nil, nil},
		{
			"removed benchmarks are listed for review",
			map[string][]float64{"a": {1}, "b": {1}},
			map[string][]float64{"a": {1}},
			nil,
			nil,
			[]string{"b"},
		},
		{
			"a skipped benchmark fails",
			map[string][]float64{"a": {1}},
			map[string][]float64{"a": {1}},
			[]string{"BenchmarkSlow"},
			[]string{"skipped benchmark: BenchmarkSlow (every benchmark must run in CI)"},
			nil,
		},
		{
			"every measurement gone fails",
			map[string][]float64{"a": {1}},
			map[string][]float64{},
			nil,
			[]string{"no benchmark measurements left, but the base had some"},
			[]string{"a"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems, removed := gate(c.base, c.head, c.skipped)
			if diff := cmp.Diff(c.problems, problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(c.removed, removed); diff != "" {
				t.Errorf("removed (-want +got):\n%s", diff)
			}
		})
	}
}
