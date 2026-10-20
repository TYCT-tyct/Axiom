package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// git runs git in root and returns what it printed, failing the test if git fails.
func git(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).CombinedOutput() // #nosec G204 -- the test's own arguments
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// TestRemoveWorktree runs git: the gate's worktree goes away, also when its directory is already gone; a registration
// git refuses to remove is reported with git's failure; and another session's worktree, even a stale one, is untouched.
func TestRemoveWorktree(t *testing.T) {
	type outcome struct{ Failed, CarriesGitFailure, StillListed, DirectoryGone, StaleKept bool }
	cases := []struct {
		name    string
		prepare func(tree string) error
		refused bool
	}{
		{"intact", func(string) error { return nil }, false},
		{"directory already gone", os.RemoveAll, false},
		// Without its .git file, git cannot validate the worktree and refuses to remove it, even with --force twice.
		{"git refuses", func(tree string) error { return os.Remove(filepath.Join(tree, ".git")) }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, scratch := t.TempDir(), t.TempDir()
			tree, stale := filepath.Join(scratch, "tree"), filepath.Join(scratch, "stale")
			git(t, root, "init", "-q")
			git(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base")
			git(t, root, "worktree", "add", "-q", "--detach", tree)
			git(t, root, "worktree", "add", "-q", "--detach", stale)
			if err := errors.Join(os.RemoveAll(stale), c.prepare(tree)); err != nil {
				t.Fatal(err)
			}

			err := removeWorktree(t.Context(), root, tree)
			list := git(t, root, "worktree", "list", "--porcelain")
			_, statErr := os.Stat(tree)
			var exitErr *exec.ExitError
			got := outcome{err != nil, errors.As(err, &exitErr), listed(list, tree), os.IsNotExist(statErr), listed(list, stale)}
			want := outcome{c.refused, c.refused, c.refused, true, true}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("after removal (-want +got):\n%s\nerror: %v", diff, err)
			}
		})
	}
}

func TestListed(t *testing.T) {
	list := []byte("worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /tmp/benchgate1/base\nHEAD def\ndetached\nlocked\n")
	if !listed(list, filepath.FromSlash("/tmp/benchgate1/base")) || listed(list, filepath.FromSlash("/tmp/benchgate2/base")) {
		t.Error("listed matches the wrong worktree")
	}
}
