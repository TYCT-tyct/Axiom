package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/config"
	"axiom/internal/platform/pg"
)

// openBalance gives an account its balance row, with no room below zero, in the transaction that opens the account
// (internal/accounts/README.md「注册、登录与会话」): without the row the account can reserve nothing. The floor is the
// site's default, 0 until a site setting says otherwise (docs/domains/billing.md「余额底线」).
func openBalance(ctx context.Context, tx pg.Tx, site, account uuid.UUID) error {
	return billing.OpenBalance(ctx, tx, site, account, 0)
}

const (
	siteUsage = "usage: axiom site init --name NAME --currency CODE --console HOST --api HOST [--admin HOST] [--key-prefix PREFIX]"
	userUsage = "usage: axiom user create|set-password --email ADDRESS < password | axiom user grant-platform --email ADDRESS [--role NAME] | axiom user reset-mfa --email ADDRESS"
)

// flags parses args as the flags define declares, naming usage on a mistake; anything left over is a mistake too.
func flags(name, usage string, args []string, define func(*flag.FlagSet)) error {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	define(set)
	if err := set.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	if set.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n%s", set.Arg(0), usage)
	}
	return nil
}

// siteCommand runs `axiom site init`: the main site, with its first tier and the domains of its surfaces. It prints the
// site's ID. The platform scope is built here, in cmd/, as for `axiom catalog`.
func siteCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "init" {
		return errors.New(siteUsage)
	}
	var name, currency, prefix, console, api, admin string
	if err := flags("site init", siteUsage, args[1:], func(set *flag.FlagSet) {
		set.StringVar(&name, "name", "", "the site's name")
		set.StringVar(&currency, "currency", "", "the accounting currency, such as USD")
		set.StringVar(&prefix, "key-prefix", "sk-ax-v1-", "what API keys start with")
		set.StringVar(&console, "console", "", "the console's host")
		set.StringVar(&api, "api", "", "the model API's host")
		set.StringVar(&admin, "admin", "", "the admin's host")
	}); err != nil {
		return err
	}
	code, err := platform.ParseCurrency(currency)
	if err != nil {
		return fmt.Errorf("--currency: %w\n%s", err, siteUsage)
	}
	spec := accounts.SiteSpec{
		Name: name, Currency: code, KeyPrefix: prefix,
		Hosts: map[accounts.Surface]string{accounts.Console: console, accounts.API: api, accounts.Admin: admin},
	}
	return withOperator(ctx, environ, func(o *accounts.Operator) error {
		site, err := o.InitSite(ctx, spec)
		if err != nil {
			return fmt.Errorf("site init: %w", err)
		}
		return writeLine(out, map[string]any{"site": site.TypeID()})
	})
}

// userArgs are what a user verb works from.
type userArgs struct{ email, role, password string }

// userVerb is one `axiom user` verb; takesPassword says it reads a password from standard input.
type userVerb struct {
	takesPassword bool
	run           func(ctx context.Context, o *accounts.Operator, a userArgs, out io.Writer) error
}

var userVerbs = map[string]userVerb{
	"create": {true, func(ctx context.Context, o *accounts.Operator, a userArgs, out io.Writer) error {
		user, account, err := o.CreateUser(ctx, a.email, a.password)
		if err != nil {
			return fmt.Errorf("user create: %w", err)
		}
		return writeLine(out, map[string]any{"user": user.TypeID(), "account": account.TypeID()})
	}},
	"grant-platform": {false, func(ctx context.Context, o *accounts.Operator, a userArgs, out io.Writer) error {
		return doneLine(out, "grant-platform", o.GrantPlatform(ctx, a.email, a.role))
	}},
	"set-password": {true, func(ctx context.Context, o *accounts.Operator, a userArgs, out io.Writer) error {
		return doneLine(out, "set-password", o.SetPassword(ctx, a.email, a.password))
	}},
	"reset-mfa": {false, func(ctx context.Context, o *accounts.Operator, a userArgs, out io.Writer) error {
		return doneLine(out, "reset-mfa", o.ResetMFA(ctx, a.email))
	}},
}

// userCommand runs `axiom user <verb> --email ADDRESS` on the main site's users. A password is read from standard
// input, so it is neither in the process list nor in a shell's history.
func userCommand(ctx context.Context, environ, args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(userUsage)
	}
	verb, ok := userVerbs[args[0]]
	if !ok {
		return fmt.Errorf("unknown verb %q\n%s", args[0], userUsage)
	}
	var a userArgs
	if err := flags("user "+args[0], userUsage, args[1:], func(set *flag.FlagSet) {
		set.StringVar(&a.email, "email", "", "the user's email address")
		set.StringVar(&a.role, "role", "admin", "the platform role to grant")
	}); err != nil {
		return err
	}
	if a.email == "" {
		return fmt.Errorf("--email is required\n%s", userUsage)
	}
	if verb.takesPassword {
		var err error
		if a.password, err = readPassword(stdin); err != nil {
			return err
		}
	}
	return withOperator(ctx, environ, func(o *accounts.Operator) error { return verb.run(ctx, o, a, out) })
}

// doneLine reports a verb that has no result to print: its failure, or that it was done.
func doneLine(out io.Writer, verb string, err error) error {
	if err != nil {
		return fmt.Errorf("user %s: %w", verb, err)
	}
	return writeLine(out, map[string]any{"done": verb})
}

// maxPasswordInput is room for the longest password (256 characters, up to four bytes each) and its line ending.
const maxPasswordInput = 256*4 + 2

// readPassword reads the password from stdin: all of it, less one trailing line ending.
func readPassword(stdin io.Reader) (string, error) {
	text, err := io.ReadAll(io.LimitReader(stdin, maxPasswordInput+1))
	if err != nil {
		return "", fmt.Errorf("read the password from standard input: %w", err)
	}
	if len(text) > maxPasswordInput {
		return "", errors.New("the password on standard input is too long")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(text), "\n"), "\r")
	if password == "" {
		return "", errors.New("the password is read from standard input, and it is empty")
	}
	return password, nil
}

// withOperator opens the database and the accounts' command-line access to it, and closes the database after do.
func withOperator(ctx context.Context, environ []string, do func(*accounts.Operator) error) error {
	env := config.New(environ)
	databaseURL := env.Required("AXIOM_DATABASE_URL")
	startupTimeout := env.Duration("AXIOM_STARTUP_TIMEOUT", defaultStartupTimeout)
	if err := env.Problems(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	db, err := openDatabase(ctx, databaseURL, startupTimeout)
	if err != nil {
		return err
	}
	defer db.Close()
	return do(accounts.NewOperator(db, pg.PlatformScope(), openBalance))
}
