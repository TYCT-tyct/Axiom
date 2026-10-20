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
	}}
}

type keyringArgs struct {
	verb                  string
	version               uint32
	batchSize, maxBatches int
}

func parseKeyringArgs(args []string) (keyringArgs, error) {
	var a keyringArgs
	if len(args) == 0 || !slices.Contains([]string{"check", "add", "activate", "retire", "rewrap"}, args[0]) {
		return a, errors.New(keyringUsage)
	}
	a.verb = args[0]
	var version string
	if err := flags("keyring", keyringUsage, args[1:], func(set *flag.FlagSet) {
		if a.verb == "rewrap" {
			set.IntVar(&a.batchSize, "batch-size", 100, "envelopes per transaction, 1 to 1000")
			set.IntVar(&a.maxBatches, "max-batches", 0, "stop after this many committed batches; zero runs to completion")
		} else if a.verb != "check" {
			set.StringVar(&version, "version", "", "positive uint32 version")
		}
	}); err != nil {
		return a, err
	}
	if a.verb == "rewrap" && (a.batchSize < 1 || a.batchSize > 1000 || a.maxBatches < 0) {
		return a, errors.New("keyring: batch size must be 1 to 1000 and max-batches nonnegative")
	}
	if a.verb != "rewrap" && a.verb != "check" {
		n, err := strconv.ParseUint(version, 10, 32)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != version {
			return a, errors.New("keyring: --version must be a canonical positive uint32")
		}
		a.version = uint32(n)
	}
	return a, nil
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
	for batch := 1; ; batch++ {
		count, err := r.RewrapBatch(ctx, a.batchSize)
		if err != nil {
			return err
		}
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
