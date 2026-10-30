package pg_test

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestOpenSizesThePool: the pool is PoolSize whatever the machine's cores, and a connection string that sets pgx's
// pool_max_conns sizes it instead.
func TestOpenSizesThePool(t *testing.T) {
	t.Parallel()
	app := pgtest.New(t).App
	cases := map[string]struct {
		poolMaxConns string
		want         int32
		reserve      int
	}{
		"the program's size":           {"", pg.PoolSize, pg.CompletionReserve},
		"the connection string":        {"&pool_max_conns=7", 7, 2},
		"minimum fills ordinary share": {"&pool_max_conns=7&pool_min_conns=5&pool_min_idle_conns=5", 7, 2},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var opts []pg.Option
			if c.poolMaxConns != "" {
				opts = append(opts, pg.WithCompletionReserve(c.reserve))
			}
			db, err := pg.Open(t.Context(), app+c.poolMaxConns, opts...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			if got := db.MaxConns(); got != c.want {
				t.Errorf("pool size %d, want %d", got, c.want)
			}
			if got := db.CompletionConns(); got != c.reserve {
				t.Errorf("completion reserve %d, want %d", got, c.reserve)
			}
			requireNoBudget(t, db)
		})
	}
}

// requireNoBudget checks that a pool sized by the connection string or PoolSize reports no division to explain.
func requireNoBudget(t *testing.T, db *pg.DB) {
	t.Helper()
	if _, ok := db.Budget(); ok {
		t.Error("Open reported a budget without a node count")
	}
}

// TestOpenClearsAnOperationsPoolMinimum: a command outside a node keeps the deployment's pool ceiling but opens no
// connection until it needs one. The four slots the budget keeps outside the nodes are shared by the migration, the
// command, the backup and a spare, so pre-opening all four for one command would deny the others.
func TestOpenClearsAnOperationsPoolMinimum(t *testing.T) {
	t.Parallel()
	app := pgtest.New(t).App
	db, err := pg.Open(t.Context(), app+"&pool_max_conns=8&pool_min_conns=8&pool_min_idle_conns=8",
		pg.WithOperationsPool(), pg.WithCompletionReserve(2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if got := db.MaxConns(); got != 8 || db.CompletionConns() != 2 {
		t.Errorf("pool = %d/%d, want 8/2", got, db.CompletionConns())
	}
	if minConns, minIdle := db.PoolMinimums(); minConns != 0 || minIdle != 0 {
		t.Errorf("minimums = %d/%d, want 0/0", minConns, minIdle)
	}
}

// TestOperationsPoolLeavesRoomForTheReservedWork: at a 20-connection server, two nodes hold six connections each (the
// budget's pool plus the three outside it), and the application role may use the 17 slots left after the three
// superuser ones. Four of those 17 are shared by a migration, a command, a backup and a spare. The role's CONNECTION
// LIMIT stands in for the server limit so the test can exhaust the same arithmetic without lowering max_connections;
// the command must not pre-open the whole reserve and leave the migration or backup out.
func TestOperationsPoolLeavesRoomForTheReservedWork(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	probeURL := reservedWorkRole(t, database)
	held := holdConnections(t, probeURL, 12)
	t.Cleanup(func() {
		for _, conn := range held {
			if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Logf("close held connection: %v", err)
			}
		}
	})
	separator := "?"
	if strings.Contains(probeURL, "?") {
		separator = "&"
	}
	db, err := pg.Open(t.Context(), probeURL+separator+"pool_max_conns=8&pool_min_conns=4&pool_min_idle_conns=4",
		pg.WithOperationsPool(), pg.WithCompletionReserve(2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if minConns, minIdle := db.PoolMinimums(); minConns != 0 || minIdle != 0 {
		t.Fatalf("operations pool minimums = %d/%d, want 0/0", minConns, minIdle)
	}
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		var one int
		return tx.QueryRow(t.Context(), "SELECT 1").Scan(&one)
	}); err != nil {
		t.Fatalf("command connection: %v", err)
	}
	held = append(held, holdConnections(t, probeURL, 2)...)
}

// reservedWorkRole creates a login role whose connection limit stands in for the app-role slots of a 20-connection
// server after its three superuser slots, and returns the database URL for it.
func reservedWorkRole(t *testing.T, database pgtest.Database) string {
	t.Helper()
	app, err := url.Parse(database.App)
	if err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("budget_ops_%d", time.Now().UnixNano())
	roleName := pgx.Identifier{role}.Sanitize()
	if err := pgtest.Run(t.Context(), database.Superuser,
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'budget' CONNECTION LIMIT 17", roleName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pgtest.Run(context.WithoutCancel(t.Context()), database.Superuser,
			fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
			t.Logf("drop role %s: %v", role, err)
		}
	})
	app.User = url.UserPassword(role, "budget")
	return app.String()
}

// holdConnections opens count connections as the test role and keeps them for the rest of the test.
func holdConnections(t *testing.T, databaseURL string, count int) []*pgx.Conn {
	t.Helper()
	held := make([]*pgx.Conn, 0, count)
	for i := range count {
		conn, err := pgx.Connect(t.Context(), databaseURL)
		if err != nil {
			t.Fatalf("connection %d: %v", i+1, err)
		}
		held = append(held, conn)
	}
	return held
}

// TestOpenRefusesANonPositiveNodeCount: zero is not "no node count" when the option was given, and neither zero nor a
// negative count reaches the connection.
func TestOpenRefusesANonPositiveNodeCount(t *testing.T) {
	t.Parallel()
	for _, nodes := range []int{0, -1} {
		_, err := pg.Open(t.Context(), "postgres://planted:planted@127.0.0.1:1/planted?sslmode=disable", pg.WithNodes(nodes))
		if err == nil {
			t.Fatalf("%d nodes: accepted", nodes)
		}
		if !strings.Contains(err.Error(), "node count") || strings.Contains(err.Error(), "planted") {
			t.Errorf("%d nodes: unsafe or unhelpful error: %v", nodes, err)
		}
	}
}

// databaseLimits reads the server limits the budget is made from.
func databaseLimits(t *testing.T, db *pg.DB) (maxConnections, reserved int) {
	t.Helper()
	err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT current_setting('max_connections')::int,
			current_setting('superuser_reserved_connections')::int +
			coalesce(current_setting('reserved_connections', true), '0')::int`).Scan(&maxConnections, &reserved)
	})
	if err != nil {
		t.Fatal(err)
	}
	return maxConnections, reserved
}

// TestOpenSizesThePoolFromTheDatabaseBudget: given the node count, Open reads the server's max_connections and its
// reserved slots, divides what is left between the nodes and takes the three connections outside the pools from each
// node's share. The combined pool ceiling is that share, or PoolSize when the share is larger.
func TestOpenSizesThePoolFromTheDatabaseBudget(t *testing.T) {
	t.Parallel()
	app := pgtest.New(t).App
	for _, nodes := range []int{1, 2, 20} {
		t.Run(strconv.Itoa(nodes), func(t *testing.T) {
			t.Parallel()
			db, err := pg.Open(t.Context(), app, pg.WithNodes(nodes))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			budget, ok := db.Budget()
			if !ok {
				t.Fatal("Open did not report a budget")
			}
			maxConnections, reserved := databaseLimits(t, db)
			allowance := (maxConnections - reserved - pg.OperationsReserve) / nodes
			want := pg.Budget{
				MaxConnections: maxConnections, Reserved: reserved + pg.OperationsReserve, Nodes: nodes,
				Allowance: allowance, Share: allowance - pg.OutsidePoolConns,
				Pool: min(allowance-pg.OutsidePoolConns, pg.PoolSize), Completion: pg.CompletionReserve,
			}
			if budget != want {
				t.Errorf("budget = %+v, want %+v", budget, want)
			}
			if got := int(db.MaxConns()); got != want.Pool {
				t.Errorf("pool ceiling %d, want %d", got, want.Pool)
			}
		})
	}
}

// TestOpenKeepsAPoolBelowTheNodeShare: a connection string may spend less than the node's share, as a deployment
// tuning a quieter node does, and the reported budget then shows the smaller pool.
func TestOpenKeepsAPoolBelowTheNodeShare(t *testing.T) {
	t.Parallel()
	app := pgtest.New(t).App
	probe, err := pg.Open(t.Context(), app, pg.WithNodes(2))
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := probe.Budget()
	probe.Close()
	db, err := pg.Open(t.Context(), app+"&pool_max_conns=80", pg.WithNodes(2), pg.WithCompletionReserve(2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	budget, ok := db.Budget()
	if !ok || budget.Pool != 80 || budget.Share != shared.Share || budget.Completion != 2 {
		t.Errorf("budget = %+v (reported %v), want pool 80 within share %d", budget, ok, shared.Share)
	}
	if got := db.MaxConns(); got != 80 || db.CompletionConns() != 2 {
		t.Errorf("pool = %d/%d, want 80/2", got, db.CompletionConns())
	}
}

// TestOpenRefusesAPoolAboveTheNodeShare: an explicit pool_max_conns cannot take more than this node's share of the
// database, and the error names the variable and the numbers without the URL.
func TestOpenRefusesAPoolAboveTheNodeShare(t *testing.T) {
	t.Parallel()
	app := pgtest.New(t).App
	probe, err := pg.Open(t.Context(), app, pg.WithNodes(20))
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := probe.Budget()
	probe.Close()
	tooLarge := strconv.Itoa(shared.Share + 1)
	_, err = pg.Open(t.Context(), app+"&pool_max_conns="+tooLarge, pg.WithNodes(20))
	if err == nil {
		t.Fatal("a pool above the node's share was accepted")
	}
	for _, want := range []string{pg.DatabaseURLVar, "pool_max_conns " + tooLarge, "exceeds", "20 nodes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("budget error %q omits %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "axiom_app") {
		t.Errorf("the error quotes the connection string: %v", err)
	}
}

// TestOpenRefusesANodeCountThatCannotFit: more nodes than the database has connection slots refuse the start and say
// what the server allows, rather than opening a pool the deployment cannot use.
func TestOpenRefusesANodeCountThatCannotFit(t *testing.T) {
	t.Parallel()
	_, err := pg.Open(t.Context(), pgtest.New(t).App, pg.WithNodes(1_000_000))
	if err == nil {
		t.Fatal("a budget that cannot fit was accepted")
	}
	for _, want := range []string{pg.DatabaseURLVar, "max_connections", "reserved slots", "outside the pools", "completions"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("budget error %q omits %q", err, want)
		}
	}
}

type poolBudgetCase struct {
	maxConnections, reserved, nodes, completion int
	want                                        pg.Budget
	wantErr                                     []string
}

// checkPoolBudget compares one case's numbers, or the named parts of its refusal.
func checkPoolBudget(t *testing.T, c poolBudgetCase) {
	t.Helper()
	got, err := pg.SizePool(c.maxConnections, c.reserved, c.nodes, c.completion)
	if len(c.wantErr) == 0 {
		if err != nil || got != c.want {
			t.Errorf("budget = %+v, %v; want %+v", got, err, c.want)
		}
		return
	}
	if err == nil {
		t.Fatalf("budget = %+v, want an error", got)
	}
	for _, want := range c.wantErr {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("budget error %q omits %q", err, want)
		}
	}
}

// TestPoolBudgetArithmetic covers the numbers the server feeds into the budget, including the per-node ceiling and the
// point where the completion reserve no longer fits.
func TestPoolBudgetArithmetic(t *testing.T) {
	t.Parallel()
	cases := map[string]poolBudgetCase{
		"one node keeps the per-node ceiling": {
			maxConnections: 100, reserved: 7, nodes: 1, completion: 16,
			want: pg.Budget{MaxConnections: 100, Reserved: 7, Nodes: 1, Allowance: 93, Share: 90, Pool: 64, Completion: 16},
		},
		"two nodes share the rest": {
			maxConnections: 100, reserved: 7, nodes: 2, completion: 16,
			want: pg.Budget{MaxConnections: 100, Reserved: 7, Nodes: 2, Allowance: 46, Share: 43, Pool: 43, Completion: 16},
		},
		"four nodes": {
			maxConnections: 100, reserved: 7, nodes: 4, completion: 16,
			want: pg.Budget{MaxConnections: 100, Reserved: 7, Nodes: 4, Allowance: 23, Share: 20, Pool: 20, Completion: 16},
		},
		"a larger database does not raise the per-node ceiling": {
			maxConnections: 1000, reserved: 7, nodes: 1, completion: 16,
			want: pg.Budget{MaxConnections: 1000, Reserved: 7, Nodes: 1, Allowance: 993, Share: 990, Pool: 64, Completion: 16},
		},
		"a smaller completion reserve fits one more node": {
			maxConnections: 100, reserved: 7, nodes: 5, completion: 4,
			want: pg.Budget{MaxConnections: 100, Reserved: 7, Nodes: 5, Allowance: 18, Share: 15, Pool: 15, Completion: 4},
		},
		"five nodes no longer fit the default completion reserve": {
			maxConnections: 100, reserved: 7, nodes: 5, completion: 16,
			wantErr: []string{"max_connections 100", "7 reserved slots", "5 nodes", "3 outside the pools", "16 for completions"},
		},
		"a share equal to the completion reserve leaves no ordinary connection": {
			maxConnections: 45, reserved: 7, nodes: 2, completion: 16,
			wantErr: []string{"max_connections 45", "2 nodes", "16 for completions"},
		},
		"no room outside the pools": {
			maxConnections: 10, reserved: 7, nodes: 1, completion: 16,
			wantErr: []string{"max_connections 10", "3 connections per node"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkPoolBudget(t, c)
		})
	}
}

// Bad budgets fail before opening a connection, without quoting the URL.
func TestOpenRefusesInvalidConnectionBudgets(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params  string
		reserve int
		want    []string
	}{
		"zero reserve": {"", 0, nil}, "negative reserve": {"", -1, nil},
		"no ordinary share": {"", pg.PoolSize, nil}, "overflow": {"", math.MaxInt, nil},
		"URL too small":                       {"&pool_max_conns=2", 2, nil},
		"minimum exceeds ordinary share":      {"&pool_max_conns=4&pool_min_conns=4", 1, []string{"pool_min_conns 4", "pool_min_idle_conns 0", "ordinary pool capacity 3"}},
		"idle minimum exceeds ordinary share": {"&pool_max_conns=4&pool_min_idle_conns=4", 1, []string{"pool_min_conns 0", "pool_min_idle_conns 4", "ordinary pool capacity 3"}},
	} {
		t.Run(name, func(t *testing.T) {
			db, err := pg.Open(t.Context(), "postgres://planted:planted@127.0.0.1:1/planted?sslmode=disable"+tc.params, pg.WithCompletionReserve(tc.reserve))
			if err == nil {
				db.Close()
				t.Fatal("invalid budget accepted")
			}
			if !strings.Contains(err.Error(), pg.DatabaseURLVar) || strings.Contains(err.Error(), "planted") {
				t.Fatalf("unsafe or unhelpful error: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("budget error %q omits %q", err, want)
				}
			}
		})
	}
}

// TestOpenRedactsTheBudgetConnection: reading the budget needs a connection of its own, and its failure names the
// variable, not the URL or its password.
func TestOpenRedactsTheBudgetConnection(t *testing.T) {
	t.Parallel()
	_, err := pg.Open(t.Context(), "postgres://planted:planted@127.0.0.1:1/planted?sslmode=disable", pg.WithNodes(1))
	if err == nil {
		t.Fatal("a budget was read from a server that is not there")
	}
	if !strings.Contains(err.Error(), pg.DatabaseURLVar) || strings.Contains(err.Error(), "planted") {
		t.Fatalf("unsafe or unhelpful error: %v", err)
	}
}

func TestCompletionPoolStillEnforcesTenantScope(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	ctx := pg.CompletionContext(t.Context())
	for _, tc := range []struct {
		scope pg.Scope
		want  int
	}{
		{pg.NewScope(true, nil, nil), 4},
		{pg.AccountScope(account1), 1},
		{pg.Scope{}, 0},
	} {
		var count int
		if err := db.Tx(ctx, tc.scope, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM rlstest.items").Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		if count != tc.want {
			t.Fatalf("completion saw %d rows, want %d", count, tc.want)
		}
	}
}
