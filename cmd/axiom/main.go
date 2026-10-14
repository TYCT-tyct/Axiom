// Command axiom is the only Axiom program: every node runs it, each with the node roles assigned to that node.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "version" {
		fmt.Fprintln(os.Stderr, "usage: axiom version")
		os.Exit(2)
	}
	fmt.Println(version())
}

// version names the build by the commit it came from, so a running node can be matched to its source. The Go
// toolchain stamps the main module's version from VCS: a pseudo-version with the commit time and hash, plus
// "+dirty" when the tree had uncommitted changes.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "axiom (no build info)"
	}
	return fmt.Sprintf("axiom %s (%s)", info.Main.Version, info.GoVersion)
}
