package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext" //nolint:depguard // tools is its own module, which cannot import axiom/internal
	"encoding/json/v2"       //nolint:depguard // tools is its own module, which cannot import axiom/internal
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// diff is the difference the gate decides from: the files that changed between the base commit and the working tree.
type diff struct {
	files []string // paths relative to the repository root, as git prints them
	all   bool     // the files could not be read, so nothing can be attributed and every package runs
}

// changedFiles lists the files that differ between the working tree and the base commit, from their merge base: the
// change under test, without the commits main moved on after the branch forked. The working tree is what the gate
// measures; in CI it is HEAD.
func changedFiles(ctx context.Context, root, base string) (diff, error) {
	mergeBase, err := exec.CommandContext(ctx, "git", "-C", root, "merge-base", "--end-of-options", base, "HEAD").Output() // #nosec G204 G702 -- the arguments reach git directly, and --end-of-options keeps a branch name that starts with a dash from being read as an option
	if err != nil {
		return diff{}, fmt.Errorf("find the merge base of %s and HEAD: %w", base, err)
	}
	// --no-renames names a rename as the file it deletes and the file it adds, so the directory the file came from is
	// attributed too; -z keeps a path with a space or a newline whole.
	out, err := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-only", "--no-renames", "-z", strings.TrimSpace(string(mergeBase))).Output() // #nosec G204 G702 -- a commit hash git printed, and fixed flags
	if err != nil {
		return diff{}, fmt.Errorf("list the files changed between %s and the working tree: %w", strings.TrimSpace(string(mergeBase)), err)
	}
	var d diff
	for file := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if file != "" {
			d.files = append(d.files, file)
		}
	}
	return d, nil
}

// runsEverything reports whether a changed file can move every package's allocation counts and so cannot be attributed
// to one: the module files decide how each package resolves and compiles; the justfile, ci.yml and the setup action
// decide the toolchain, flags and environment the gate runs in; and the gate's own code decides what it measures, so a
// change to it is checked against the real benchmarks, not skipped.
func runsEverything(file string) bool {
	switch path.Base(file) {
	case "go.mod", "go.sum", "go.work":
		return true
	}
	return file == "justfile" || file == ".github/workflows/ci.yml" ||
		strings.HasPrefix(file, ".github/actions/setup/") || strings.HasPrefix(file, "tools/benchgate/")
}

// affects reports whether the difference can move a benchmark package's allocation counts. A changed file inside the
// directory of a package its test binary compiles, or below it — the package's testdata, and the files it embeds, live
// there — changes what the binary measures. A file that runsEverything cannot be attributed to a package and affects
// every package. closures holds the package's test closure on each side; a side where the package does not exist
// contributes none.
func (d diff) affects(closures ...closure) bool {
	return d.all || slices.ContainsFunc(d.files, runsEverything) || slices.ContainsFunc(d.files, func(file string) bool {
		return slices.ContainsFunc(closures, func(c closure) bool { return c.contains(file) })
	})
}

// closure is what a benchmark package's test binary compiles inside the tree: the directories of the packages of its
// test import closure, relative to the repository root.
type closure []string

// contains reports whether a changed file lies in the directory of one of the closure's packages or below it.
func (c closure) contains(file string) bool {
	for _, dir := range c {
		if dir == "." || file == dir || strings.HasPrefix(file, dir+"/") {
			return true
		}
	}
	return false
}

// selectPackages reads the benchmark packages of both trees and returns the ones each side measures: those whose
// benchmarks the change can move, in the go command's order. A package the change cannot reach is left out of both
// sides, so it is never compared, which is not a removal: nothing was measured for it to remove.
func selectPackages(ctx context.Context, tree, root, base string, change diff) (baseTargets, headTargets, skipped []string, err error) {
	var baseSide, headSide side
	if baseSide.packages, err = benchmarkPackages(ctx, tree); err != nil {
		return nil, nil, nil, fmt.Errorf("list the benchmarks of base commit %s: %w", base, err)
	}
	if headSide.packages, err = benchmarkPackages(ctx, root); err != nil {
		return nil, nil, nil, fmt.Errorf("list the benchmarks of the working tree: %w", err)
	}
	if baseSide.closures, err = closures(ctx, tree, "base commit "+base, baseSide.packages); err != nil {
		return nil, nil, nil, err
	}
	if headSide.closures, err = closures(ctx, root, "the working tree", headSide.packages); err != nil {
		return nil, nil, nil, err
	}
	baseTargets, headTargets, skipped = plan(change, baseSide, headSide)
	return baseTargets, headTargets, skipped, nil
}

// side is what one tree offers the plan: its benchmark packages, in the go command's order, and their test closures.
type side struct {
	packages []string
	closures map[string]closure
}

// plan keeps the packages the change can reach and names the ones it leaves out, once, whichever sides have them.
func plan(change diff, base, head side) (baseTargets, headTargets, skipped []string) {
	measured := map[string]bool{}
	for _, s := range []side{base, head} {
		for _, pkg := range s.packages {
			if _, ok := measured[pkg]; !ok {
				measured[pkg] = change.affects(base.closures[pkg], head.closures[pkg])
			}
		}
	}
	for _, pkg := range base.packages {
		if measured[pkg] {
			baseTargets = append(baseTargets, pkg)
		}
	}
	for _, pkg := range head.packages {
		if measured[pkg] {
			headTargets = append(headTargets, pkg)
		}
	}
	for pkg := range measured {
		if !measured[pkg] {
			skipped = append(skipped, pkg)
		}
	}
	sort.Strings(skipped)
	return baseTargets, headTargets, skipped
}

// closures reads what the test binary of every package in pkgs compiles, with one `go list -deps -test` for the tree:
// the go command's own answer, the same packages cmd/go builds into each binary. Packages outside the tree — the
// standard library and the module cache — cannot change with it and are left out, and the directories kept are relative
// to it, as git names the changed files. label names the side in errors.
func closures(ctx context.Context, tree, label string, pkgs []string) (map[string]closure, error) {
	cmd := exec.CommandContext(ctx, "go", append([]string{"list", "-deps", "-test", "-json"}, pkgs...)...) //nolint:gosec // pkgs are import paths the go command listed for this tree; the other arguments are fixed flags
	cmd.Dir, cmd.Stderr = tree, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list the test closures of %s: %w", label, err)
	}
	listing, err := readListing(out, tree)
	if err != nil {
		return nil, fmt.Errorf("decode the test closures of %s: %w", label, err)
	}
	found := make(map[string]closure, len(pkgs))
	for _, pkg := range pkgs {
		test, ok := listing.deps[pkg+".test"]
		if !ok {
			return nil, fmt.Errorf("the go command listed no test binary for %s of %s", pkg, label)
		}
		// A benchmark package outside the tree (a symlinked workspace makes go list and the tree disagree on the prefix)
		// would get an empty closure, no change would ever reach it, and the gate would skip it for good.
		if _, ok := listing.dirs[pkg]; !ok {
			return nil, fmt.Errorf("the directory of benchmark package %s is not inside %s of %s", pkg, tree, label)
		}
		found[pkg] = listing.closure(pkg, test)
	}
	return found, nil
}

// listing is what one `go list -deps -test -json` pass said about the tree.
type listing struct {
	dirs map[string]string   // import path to its directory, relative to the tree
	deps map[string][]string // import path to everything its test binary compiles and imports
}

// readListing decodes the go command's JSON stream, keeping the packages inside the tree and the dependencies of every
// package.
func readListing(out []byte, tree string) (listing, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(out))
	l := listing{dirs: map[string]string{}, deps: map[string][]string{}}
	for {
		var p struct {
			ImportPath string
			Dir        string
			Deps       []string
		}
		if err := json.UnmarshalDecode(dec, &p); errors.Is(err, io.EOF) {
			return l, nil
		} else if err != nil {
			return listing{}, err
		}
		l.deps[p.ImportPath] = p.Deps
		if dir, ok := below(tree, p.Dir); ok {
			l.dirs[p.ImportPath] = dir
		}
	}
}

// closure is the set of directories a package brings in: its own and every tree package its test binary compiles.
func (l listing) closure(pkg string, test []string) closure {
	seen := map[string]bool{}
	var c closure
	for _, dep := range append([]string{pkg}, test...) {
		dir, ok := l.dirs[dep]
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		c = append(c, dir)
	}
	slices.Sort(c)
	return c
}

// below returns dir relative to root in the slash form git prints, and whether dir is inside root.
func below(root, dir string) (string, bool) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
