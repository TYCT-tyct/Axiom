// Command withcluster runs a command, `go test` in `just test`, with one PostgreSQL cluster that every test binary it
// starts shares (pgtest.ClusterEnv), and removes the cluster when the command ends. Containers are then as many as the
// runs in progress, not as the test packages of each run.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

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
	// Ctrl-C reaches the command too, as it shares the console; this process outlives it to remove the cluster.
	signal.Ignore(os.Interrupt)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // running the command it is given is this tool's job
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), pgtest.ClusterEnv+"="+connection)
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
	if err := terminate(stop); err != nil {
		fmt.Fprintln(os.Stderr, "withcluster: remove the cluster:", err)
	}
	return status
}
