// Command withcluster runs a command, `go test` in `just test`, with one PostgreSQL cluster and one Valkey that every
// test binary it starts shares (pgtest.ClusterEnv, kvtest.URLEnv), and removes both when the command ends. Containers
// are then as many as the runs in progress, not as the test packages of each run.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pgtest"
)

const terminateTimeout = time.Minute

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: withcluster command [args...]")
		return 2
	}
	ctx := context.Background()
	connection, terminate, err := pgtest.StartCluster(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "withcluster:", err)
		return 1
	}
	// The cache is one container for the run, as the cluster is: a test that changes its state starts its own
	// (internal/platform/kv/README.md「测试」). Its URL and the two password files go in the environment the same way, so
	// that none enters a test's cache key; the second password is the read-only admin a test reads a lifetime or lists
	// keys with, which must not cost a container.
	url, passwordFile, adminPasswordFile, terminateCache, err := kvtest.StartServer(ctx)
	if err != nil {
		stop, cancel := context.WithTimeout(context.Background(), terminateTimeout)
		defer cancel()
		if err := terminate(stop); err != nil {
			fmt.Fprintln(os.Stderr, "withcluster: remove the cluster:", err)
		}
		fmt.Fprintln(os.Stderr, "withcluster: start valkey:", err)
		return 1
	}
	// Ctrl-C reaches the command too, as it shares the console; this process outlives it to remove the containers.
	signal.Ignore(os.Interrupt)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // running the command it is given is this tool's job
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		pgtest.ClusterEnv+"="+connection, kvtest.URLEnv+"="+url, kvtest.PasswordFileEnv+"="+passwordFile,
		kvtest.AdminPasswordFileEnv+"="+adminPasswordFile)
	// The cluster's connection keeps the command's reaper alive across idle gaps between packages (#411).
	// A changing session is a test cache input, so only pass it when there is a reaper to keep alive.
	if !testcontainers.ReadConfig().Config.RyukDisabled {
		cmd.Env = append(cmd.Env, "TESTCONTAINERS_SESSION_ID="+testcontainers.SessionID())
	}
	status := 0
	if err := cmd.Run(); err != nil {
		status = 1
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			status = exit.ExitCode()
		} else {
			fmt.Fprintln(os.Stderr, "withcluster:", err)
		}
	}
	stop, cancel := context.WithTimeout(context.Background(), terminateTimeout)
	defer cancel()
	if err := errors.Join(terminate(stop), terminateCache(stop)); err != nil {
		fmt.Fprintln(os.Stderr, "withcluster: remove the containers:", err)
	}
	return status
}
