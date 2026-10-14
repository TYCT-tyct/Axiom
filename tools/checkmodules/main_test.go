package main

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
)

func doc(rows ...string) string {
	return "# 总体架构\n\n" + tableHeading + "\n\n| 模块 | 可以引用 | 为什么 |\n|---|---|---|\n" +
		strings.Join(rows, "\n") + "\n\n## 下一节\n\n| `gateway` | `billing` | |\n"
}

// symlink marks a test file that is a symbolic link rather than a regular file.
const symlink = "<symbolic link>"

var baseTable = doc("| `platform` | — | |", "| `catalog` | — | |", "| `billing` | `catalog` | |")

func TestReadTable(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		problems []string
	}{
		{"rows up to the next heading", baseTable, nil},
		{
			"a row lists a module below it", doc("| `platform` | — | |", "| `catalog` | `billing` | |", "| `billing` | — | |"),
			[]string{"catalog may import billing, which is not a row above it"},
		},
		{
			"a row lists itself", doc("| `platform` | — | |", "| `catalog` | `catalog` | |"),
			[]string{"catalog may import catalog, which is not a row above it"},
		},
		{
			"a second row cannot reopen a module", doc("| `platform` | — | |", "| `catalog` | — | |", "| `billing` | `catalog` | |", "| `catalog` | `billing` | |"),
			[]string{"catalog has two rows"},
		},
		{"the first row is platform", doc("| `catalog` | — | |"), []string{"the first row must be platform, not catalog"}},
		{"no heading", "# 总体架构\n", []string{`docs/architecture.md: no heading "### 模块之间的依赖"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, problems := readTable(c.text)
			if diff := cmp.Diff(c.problems, problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
	table, _ := readTable(baseTable)
	want := map[string]map[string]bool{"platform": {}, "catalog": {"platform": true}, "billing": {"catalog": true, "platform": true}}
	if diff := cmp.Diff(want, table); diff != "" {
		t.Errorf("table (-want +got):\n%s", diff)
	}
}

// TestCheck runs the whole check on small repositories; each source is valid Go that a regex-based reader got wrong.
func TestCheck(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		problems []string
	}{
		{"allowed edges and the composition root", map[string]string{
			"internal/billing/billing.go": "package billing\n\nimport (\n\t\"fmt\"\n\t_ \"axiom/internal/catalog\"\n\t_ \"axiom/internal/billing/internal/ledger\"\n)\n",
			"cmd/axiom/main.go":           "package main\n\nimport _ \"axiom/internal/billing\"\n",
		}, nil},
		{"upward import in a file only an OS or a tag selects", map[string]string{
			"internal/catalog/race_windows_test.go": "//go:build race\n\npackage catalog\n\nimport _ \"axiom/internal/billing\"\n",
		}, []string{"axiom/internal/catalog imports axiom/internal/billing: catalog may not import billing"}},
		{"byte order mark, semicolons and an escaped path", map[string]string{
			"internal/catalog/a.go": "\ufeffpackage catalog;import _ \"fmt\";import _ \"\\x61xiom/internal/billing\"\n",
		}, []string{"axiom/internal/catalog imports axiom/internal/billing: catalog may not import billing"}},
		{"an escaped allowed path is allowed", map[string]string{
			"internal/billing/a.go": "package billing\n\nimport _ \"axiom/internal/\\x63atalog\"\n",
		}, nil},
		{"a bridge through testdata", map[string]string{
			"internal/catalog/a.go":                 "package catalog\n\nimport _ \"axiom/internal/catalog/testdata/bridge\"\n",
			"internal/catalog/testdata/bridge/b.go": "package bridge\n\nimport _ \"axiom/internal/billing\"\n",
		}, []string{"axiom/internal/catalog/testdata/bridge imports axiom/internal/billing: catalog may not import billing"}},
		{"a bridge through the root package or cmd/", map[string]string{
			"internal/catalog/a.go": "package catalog\n\nimport (\n\t_ \"axiom\"\n\t_ \"axiom/cmd/bridge\"\n)\n",
			"bridge.go":             "package axiom\n",
		}, []string{
			"axiom/internal/catalog imports axiom/cmd/bridge: only cmd/ may import packages outside internal/<module>/",
			"axiom/internal/catalog imports axiom: only cmd/ may import packages outside internal/<module>/",
			"axiom: Go code belongs under cmd/ or internal/<module>/",
		}},
		{
			"a module without a row",
			map[string]string{"internal/gateway/g.go": "package gateway\n"},
			[]string{`axiom/internal/gateway: module gateway has no row in the table under "### 模块之间的依赖"`},
		},
		{"nested modules are not checked", map[string]string{
			"tools/go.mod":    "module axiom/tools\n",
			"tools/x/x.go":    "package x\n\nimport _ \"axiom/internal/billing\"\n",
			"internal/c/c.go": "package c\n",
		}, []string{`axiom/internal/c: module c has no row in the table under "### 模块之间的依赖"`}},
		{"a directory named go.mod is not a module", map[string]string{
			"internal/catalog/go.mod/keep.txt": "",
			"internal/catalog/a.go":            "package catalog\n\nimport _ \"axiom/internal/billing\"\n",
		}, []string{"axiom/internal/catalog imports axiom/internal/billing: catalog may not import billing"}},
		{"vendored code is skipped and cannot be imported", map[string]string{
			"vendor/example.com/lib/lib.go":             "package lib\n\nimport _ \"axiom/internal/billing\"\n",
			"internal/catalog/a.go":                     "package catalog\n\nimport _ \"axiom/internal/catalog/node_modules/bridge\"\n",
			"internal/catalog/node_modules/bridge/b.go": "package bridge\n\nimport _ \"axiom/internal/billing\"\n",
		}, []string{"axiom/internal/catalog imports axiom/internal/catalog/node_modules/bridge: this check does not read vendor or node_modules, so nothing may import from them"}},
		{"files the go command ignores", map[string]string{
			"internal/catalog/_sample.go": "package catalog\n\nimport _ \"axiom/internal/billing\"\n",
			"internal/catalog/.hidden.go": "not Go at all",
		}, nil},
		{"a symbolic link could hide code", map[string]string{
			"internal/catalog/bridge": symlink,
		}, []string{"internal/catalog/bridge: symbolic links are not allowed in the main module"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := fstest.MapFS{"docs/architecture.md": {Data: []byte(baseTable)}}
			for name, src := range c.files {
				repo[name] = &fstest.MapFile{Data: []byte(src)}
				if src == symlink {
					repo[name] = &fstest.MapFile{Mode: fs.ModeSymlink}
				}
			}
			problems, err := check(repo)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.problems, problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}
