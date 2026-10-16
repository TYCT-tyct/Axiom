package gateway

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	if _, err := New(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
}

// Every forbidden relation of internal/gateway/README.md「超时与限额」 stops the node from starting, and the error
// names what to change.
func TestConfigRejectsForbiddenRelations(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Config)
		want   string
	}{
		"lock_timeout reaches the admission budget":    {func(c *Config) { c.LockTimeout = c.AdmissionBudget }, "LockTimeout"},
		"statement_timeout above the admission budget": {func(c *Config) { c.StatementTimeout = 2 * c.AdmissionBudget }, "StatementTimeout"},
		"queue longer than StartBy":                    {func(c *Config) { c.QueueWait = c.StartBy + time.Second }, "QueueWait"},
		"StartBy reaches the longest call":             {func(c *Config) { c.StartBy = c.MaxCall }, "StartBy"},
		"header wait reaches the longest call":         {func(c *Config) { c.HeaderWait = c.MaxCall }, "HeaderWait"},
		"idle timeout reaches the longest call":        {func(c *Config) { c.IdleTimeout = c.MaxCall }, "IdleTimeout"},
		"keep-alive not below the idle timeout":        {func(c *Config) { c.KeepAlive = c.IdleTimeout }, "KeepAlive"},
		"TTL under three heartbeats":                   {func(c *Config) { c.HolderTTL = 3*c.HolderHeartbeat - 1 }, "HolderTTL"},
		"heartbeat so large that 3x overflows":         {func(c *Config) { c.HolderHeartbeat = 1 << 62 }, "HolderTTL"},
		"heartbeat at the duration maximum":            {func(c *Config) { c.HolderHeartbeat = math.MaxInt64 }, "HolderTTL"},
		"reclaim grace under the TTL":                  {func(c *Config) { c.ReclaimGrace = c.HolderTTL - 1 }, "ReclaimGrace"},
		"settle grace not above the background retry":  {func(c *Config) { c.SettleGrace = c.SettleBackground }, "SettleGrace"},
		"settle grace not above both retries":          {func(c *Config) { c.SettleGrace = c.SettleRetry + c.SettleBackground }, "SettleGrace"},
		"batch writers do not fit outside the reserve": {func(c *Config) { c.BatchConnections = c.PoolSize - c.PoolReserve + 1 }, "BatchConnections"},
		"per-key waiters above the queue":              {func(c *Config) { c.QueuePerKey = c.QueueMax + 1 }, "QueuePerKey"},
		"zero node slots":                              {func(c *Config) { c.NodeInFlight = 0 }, "NodeInFlight"},
		"negative gate wait":                           {func(c *Config) { c.AccountGateWait = -1 }, "AccountGateWait"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.change(&cfg)
			if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

func TestConfigReportsEveryProblem(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HolderTTL, cfg.QueuePerKey = 1, cfg.QueueMax+1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "HolderTTL") || !strings.Contains(err.Error(), "QueuePerKey") {
		t.Fatalf("Validate() = %v, want both problems", err)
	}
}
