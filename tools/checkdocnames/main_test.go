package main

import (
	"go/parser"
	"go/token"
	"testing"
)

const source = `package billing

type Hold struct {
	Refund bool
}

func Settle() {}

func (h *Hold) Release() {}

var First, Second = 1, 2

const (
	MaxHolds = 3
)

func TestSettleOnce(t *testing.T) {
	Local := 1
	_ = Local
}

func BenchmarkSettle(b *testing.B) {}

func (h *Hold) TestEndpoint() {}
`

func TestProblems(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "hold.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	declare(names, file)
	declared := map[string]map[string]bool{"billing": names}
	cases := []struct {
		name, text string
		want       int
	}{
		{"test that exists", "see `TestSettleOnce`", 0},
		{"subtest of a test that exists", "`TestSettleOnce/zero`", 0},
		{"renamed test", "`TestSettleTwice` covers it", 1},
		{"prefix glob that matches", "`TestSettle*`", 0},
		{"prefix glob that matches nothing", "`TestRefund*`", 1},
		{"production code named like a test", "`TestEndpoint`", 0},
		{"one of two benchmarks missing", "`BenchmarkSettle` and `BenchmarkRefund`", 1},
		{"declared function", "`billing.Settle`", 0},
		{"declared function with arguments", "`billing.Settle(ctx, id)`", 0},
		{"method and block constant", "`billing.Release` `billing.MaxHolds`", 0},
		{"every name of a var spec", "`billing.Second`", 0},
		{"a struct field is not a declaration", "`billing.Refund`", 1},
		{"a local name is not a declaration", "`billing.Local`", 1},
		{"lowercase SQL object is left alone", "`billing.expiry_cursor`", 0},
		{"another package's names are left alone", "`http.Handler` `errors.AsType`", 0},
		{"placeholder", "`包路径.TestName`", 0},
		{"outside code formatting", "TestNothing billing.Refund", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := problems(map[string]string{"docs/x.md": c.text}, declared); len(got) != c.want {
				t.Errorf("got %q, want %d problems", got, c.want)
			}
		})
	}
	if got := problems(map[string]string{"docs/x.md": "a\n`TestGone`"}, declared); len(got) != 1 || got[0] != "docs/x.md:2: `TestGone` names test TestGone, which no Go file declares" {
		t.Errorf("got %q", got)
	}
}
