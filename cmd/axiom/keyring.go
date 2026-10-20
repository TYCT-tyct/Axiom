package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"

	"axiom/internal/platform/config"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

const keyringUsage = "usage: axiom keyring check | add|activate|retire --version N | rewrap [--batch-size 100] [--max-batches N]"

// keyRotation is the envelope inventory; module AAD is an on-disk contract, not guessed from ciphertext.
func keyRotation(db *pg.DB, path string) secret.Rotation {
	return secret.Rotation{DB: db, Scope: pg.PlatformScope(), Path: path, Stores: []secret.EnvelopeStore{
		{Schema: "accounts", Table: "users", ID: "id", Prefix: "totp_", Purpose: "totp"},
		{Schema: "suppliers", Table: "supplier_logins", ID: "supplier_id", Purpose: "login"},
		{Schema: "suppliers", Table: "credentials", ID: "id", Purpose: "key"},
		{Schema: "payments", Table: "merchant_configs", ID: "merchant_id", Purpose: "config"},
	}}
}

// keyringVerbs are the subcommands, in the order the usage line lists them.
var keyringVerbs = []string{"check", "add", "activate", "retire", "rewrap"}

type keyringArgs struct {
	verb                  string
	version               uint32
	batchSize, maxBatches int
}

func parseKeyringArgs(args []string) (keyringArgs, error) {
	var a keyringArgs
	if len(args) == 0 || !slices.Contains(keyringVerbs, args[0]) {
		return a, errors.New(keyringUsage)
	}
	a.verb = args[0]
	version, err := keyringFlags(&a, args[1:])
	if err != nil {
		return a, err
	}
	switch a.verb {
	case "check":
		return a, nil
	case "rewrap":
		if a.batchSize < 1 || a.batchSize > 1000 || a.maxBatches < 0 {
			return a, errors.New("keyring: batch size must be 1 to 1000 and max-batches nonnegative")
		}
		return a, nil
	default:
		a.version, err = parseVersion(version)
		return a, err
	}
}

// keyringFlags parses the flags of the verb, which differ: rewrap carries its batch limits and the lifecycle verbs
// take the version. It returns the version as it was written, since only the caller's verb reads it.
func keyringFlags(a *keyringArgs, args []string) (string, error) {
	var version string
	err := flags("keyring", keyringUsage, args, func(set *flag.FlagSet) {
		switch a.verb {
		case "rewrap":
			set.IntVar(&a.batchSize, "batch-size", 100, "envelopes per transaction, 1 to 1000")
			set.IntVar(&a.maxBatches, "max-batches", 0, "stop after this many committed batches; zero runs to completion")
		case "check":
		default:
			set.StringVar(&version, "version", "", "positive uint32 version")
		}
	})
	return version, err
}

// parseVersion accepts a version as a person writes it and refuses leading zeros, so one version has one spelling.
func parseVersion(version string) (uint32, error) {
	n, err := strconv.ParseUint(version, 10, 32)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != version {
		return 0, errors.New("keyring: --version must be a canonical positive uint32")
	}
	return uint32(n), nil
}

func keyringCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	a, err := parseKeyringArgs(args)
	if err != nil {
		return err
	}
	env := config.New(environ)
	databaseURL := env.Required(pg.DatabaseURLVar)
	path := env.Required(masterKeyFileVar)
	timeout := env.Duration(startupTimeoutVar, defaultStartupTimeout)
	if err := env.Problems(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	db, err := openDatabase(ctx, databaseURL, timeout)
	if err != nil {
		return err
	}
	defer db.Close()
	r := keyRotation(db, path)
	if a.verb == "check" {
		_, status, err := r.Check(ctx)
		if err != nil {
			return err
		}
		return writeLine(out, status)
	}
	switch a.verb {
	case "add":
		err = r.Add(ctx, a.version)
	case "activate":
		err = r.Activate(ctx, a.version)
	case "retire":
		err = r.Retire(ctx, a.version)
	case "rewrap":
		return rewrapCommand(ctx, r, a, out)
	}
	if err != nil {
		return err
	}
	return writeLine(out, map[string]any{"done": a.verb, "version": a.version})
}

func rewrapCommand(ctx context.Context, r secret.Rotation, a keyringArgs, out io.Writer) error {
	if _, _, err := r.Check(ctx); err != nil {
		return err
	}
	var cursor secret.RewrapCursor
	for batch := 1; ; batch++ {
		count, next, err := r.RewrapBatch(ctx, cursor, a.batchSize)
		if err != nil {
			return err
		}
		cursor = next
		if count == 0 {
			return writeLine(out, map[string]any{"done": "rewrap"})
		}
		if err := writeLine(out, map[string]any{"batch": batch, "rewrapped": count}); err != nil {
			return err
		}
		if a.maxBatches != 0 && batch >= a.maxBatches {
			return writeLine(out, map[string]any{"paused": "rewrap"})
		}
	}
}
