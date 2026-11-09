package main

import (
	"fmt"
	"io"

	"axiom/internal/observability"
)

// callLogCommand is `axiom call-log check <dir>`: it fails while the spill directory dir holds a call record the
// database has not confirmed, and says so. The backup runs it, with the node stopped and the directory's lock held, so a
// set is never taken while a settled call's record exists only on this machine (docs/ops/backup-restore.md「备份」).
func callLogCommand(args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "check" {
		return fmt.Errorf("usage: axiom call-log check <dir>")
	}
	n, err := observability.SpillBacklog(args[1])
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%s holds %d call records the database does not have yet: start the node so that it writes them, stop it, and check again", args[1], n)
	}
	_, err = fmt.Fprintf(out, "%s holds no call record the database does not have\n", args[1])
	return err
}
