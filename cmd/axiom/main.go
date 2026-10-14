// Command axiom is the only Axiom program: every node runs it, each with the node roles assigned to that node.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

const usage = "usage: axiom version"

func main() {
	if len(os.Args) != 2 || os.Args[1] != "version" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	fmt.Println(version())
}

// version names the build by the commit it was built from, so a running node can be matched to its source.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	vcs := map[string]string{}
	for _, setting := range info.Settings {
		vcs[setting.Key] = setting.Value
	}
	revision := vcs["vcs.revision"]
	if revision == "" {
		revision = "unknown"
	}
	if vcs["vcs.modified"] == "true" {
		revision += "+dirty"
	}
	return fmt.Sprintf("axiom %s (%s, %s)", revision, vcs["vcs.time"], info.GoVersion)
}
