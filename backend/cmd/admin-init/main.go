// Command admin-init grants an existing registered user an administrative
// built-in role (admin or super_admin). It is the ONLY way to bootstrap admin
// access: no default admin/admin exists (see constitution), and the command
// deliberately takes no password — the user must have registered normally.
//
// Implementation goes through the IAM application service
// (iam.GrantBuiltInAdminRole) — never the global sqlc package — so the grant
// semantics (row lock, idempotence, version bump only on change, preserving
// other roles) are exactly the ones the IAM contract tests pin down.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
)

type options struct {
	username string
	role     string
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return 2
	}

	databaseURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if databaseURL == "" {
		fmt.Fprintln(stderr, "admin-init: DATABASE_URL is required")
		return 1
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "admin-init: unable to parse DATABASE_URL")
		return 1
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(stderr, "admin-init: unable to connect to database")
		return 1
	}

	result, err := iam.NewService(postgres.NewStore(pool), slog.New(slog.NewTextHandler(io.Discard, nil))).
		GrantBuiltInAdminRole(ctx, opts.username, opts.role)
	if err != nil {
		switch {
		case errors.Is(err, iam.ErrUserNotFound):
			fmt.Fprintf(stderr, "admin-init: registered user %q was not found\n", opts.username)
		case errors.Is(err, iam.ErrRoleNotFound):
			fmt.Fprintf(stderr, "admin-init: built-in role %q was not found; run migrations first\n", opts.role)
		default:
			fmt.Fprintf(stderr, "admin-init: %v\n", err)
		}
		return 1
	}

	fmt.Fprintf(stdout, "user %q (id %d) has role %q (user version %d)\n",
		opts.username, result.UserID, opts.role, result.ResultingVersion)
	return 0
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("admin-init", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.username, "username", "", "registered username to promote")
	flags.StringVar(&opts.role, "role", "", "target role: admin or super_admin")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(output, "admin-init: positional arguments are not accepted")
		return options{}, errors.New("unexpected positional arguments")
	}

	opts.username = strings.TrimSpace(opts.username)
	opts.role = strings.TrimSpace(opts.role)
	if opts.username == "" {
		fmt.Fprintln(output, "admin-init: --username is required")
		return options{}, errors.New("username is required")
	}
	if opts.role != "admin" && opts.role != "super_admin" {
		fmt.Fprintln(output, "admin-init: --role must be admin or super_admin")
		return options{}, errors.New("invalid role")
	}
	return opts, nil
}
