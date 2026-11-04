package alloctest

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestParse checks what the parent accepts from the child's output and what it refuses.
func TestParse(t *testing.T) {
	funcs := []Function{{Name: "one"}, {Name: "two"}}
	for _, c := range []struct {
		name string
		out  string
		want map[string]Allocs
		err  string
	}{
		{
			name: "a report for every function",
			out:  "=== RUN TestMeasure\nalloctest\tone\t2\t16\nalloctest\ttwo\t0\t0\n",
			want: map[string]Allocs{"one": {Count: 2, Bytes: 16}, "two": {}},
		},
		{"the child reported nothing", "", nil, `the child did not report "one"`},
		{"a function reported twice", "alloctest\tone\t1\t8\nalloctest\tone\t1\t8\n", nil, `the child reported "one" twice`},
		{"a malformed line", "alloctest\tone\n", nil, "malformed measurement"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parse([]byte(c.out), funcs)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("parse = %v, want an error containing %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("parse (-want +got):\n%s", diff)
			}
		})
	}
}

// TestParseRetained checks the parent's reading of retention reports: one number per function, exactly once.
func TestParseRetained(t *testing.T) {
	funcs := []RetainedFunction{{Name: "one"}, {Name: "two"}}
	for _, c := range []struct {
		name string
		out  string
		want map[string]uint64
		err  string
	}{
		{
			name: "a report for every function",
			out:  "=== RUN TestMeasureRetained\nalloctest\tone\t1024\nalloctest\ttwo\t0\n",
			want: map[string]uint64{"one": 1024, "two": 0},
		},
		{"the child reported nothing", "", nil, `the child did not report "one"`},
		{"a function reported twice", "alloctest\tone\t1\nalloctest\tone\t1\n", nil, `the child reported "one" twice`},
		{"a malformed line", "alloctest\tone\n", nil, "malformed measurement"},
		{"too many numbers", "alloctest\tone\t1\t2\n", nil, "malformed measurement"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseRetained([]byte(c.out), funcs)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("parseRetained = %v, want an error containing %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("parseRetained (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRunPattern checks that every level of the test name is anchored: the go command matches each one on its own.
func TestRunPattern(t *testing.T) {
	for name, want := range map[string]string{
		"TestAllocs":                  "^TestAllocs$",
		"TestAllocs/Inspect-format=0": "^TestAllocs$/^Inspect-format=0$",
		"Test/one/two":                "^Test$/^one$/^two$",
	} {
		if got := runPattern(name); got != want {
			t.Errorf("runPattern(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestHeapGrowth: a heap that shrank between the readings grew by nothing, so it cannot fail a retention test.
func TestHeapGrowth(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after uint64
		want          uint64
	}{
		{"grew", 1 << 20, 3 << 20, 2 << 20},
		{"unchanged", 1 << 20, 1 << 20, 0},
		{"shrank", 3 << 20, 1 << 20, 0},
	} {
		if got := heapGrowth(tc.before, tc.after); got != tc.want {
			t.Errorf("%s: heapGrowth(%d, %d) = %d, want %d", tc.name, tc.before, tc.after, got, tc.want)
		}
	}
}
