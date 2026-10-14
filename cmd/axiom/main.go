// Command axiom is the only Axiom program: every node runs it, each with the node roles assigned to that node.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

const usage = "usage: axiom serve | version"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		// The first SIGINT or SIGTERM starts a graceful shutdown; stopping the notification then restores the default
		// action, so a second signal ends the process at once.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		context.AfterFunc(ctx, stop)
		logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version())
		if err := serve(ctx, os.Environ(), logger); err != nil {
			logger.Error("serve failed", "err", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version())
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
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
