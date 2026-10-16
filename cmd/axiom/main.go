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

	_ "golang.org/x/crypto/x509roots/fallback" // root certificates for the scratch image, which has none (deploy/Dockerfile)
)

const usage = "usage: axiom serve | migrate | healthcheck | version | site init | user <verb> | catalog sync | billing <listing> | check | <operation> <file>"

func main() {
	if !validArgs(os.Args) {
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
	case "migrate":
		if err := migrate(context.Background(), os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "catalog", "site", "user", "billing":
		exitOn(operate(context.Background(), os.Environ(), os.Args[1:]))
	case "healthcheck":
		if err := healthcheck(context.Background(), os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version())
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

// validArgs accepts a command, and for the commands that take arguments those arguments.
func validArgs(args []string) bool {
	return len(args) == 2 || len(args) > 2 && (args[1] == "catalog" || args[1] == "site" || args[1] == "user" || args[1] == "billing")
}

// operate runs a command of the platform's operators, which takes arguments, against AXIOM_DATABASE_URL.
func operate(ctx context.Context, environ, args []string) error {
	switch args[0] {
	case "catalog":
		return catalogCommand(ctx, environ, args[1:], os.Stdout)
	case "site":
		return siteCommand(ctx, environ, args[1:], os.Stdout)
	case "billing":
		return billingCommand(ctx, environ, args[1:], os.Stdout)
	default:
		return userCommand(ctx, environ, args[1:], os.Stdin, os.Stdout)
	}
}

// exitOn reports a failed command and ends the program with its failure status.
func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// version names the build by the commit it came from, so a running node can be matched to its source. The Go
// toolchain stamps the main module's version from VCS: a pseudo-version with the commit time and hash, plus
// "+dirty" when the tree had uncommitted changes. The console API answers with it publicly, so it leaves out the Go
// toolchain's version, which would tell an attacker which known vulnerabilities to try; go version -m shows it.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "axiom (no build info)"
	}
	return "axiom " + info.Main.Version
}
