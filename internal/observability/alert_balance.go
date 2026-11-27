package observability

import (
	"context"
	"strings"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// LowBalanceThreshold is a supplier's alert line for suppliers.low_balance: the amount, in the unit the supplier's
// balances are read in (a relay's native unit, a key's wallet currency), under which a balance alerts. A supplier
// without one is held to the default suppliers defines.
type LowBalanceThreshold struct {
	SupplierID uuid.UUID `json:"supplier_id"`
	Threshold  string    `json:"threshold"`
}

// maxLowBalanceThresholds bounds the configuration's body; it is far above the suppliers a platform connects.
const maxLowBalanceThresholds = 1000

func validateLowBalanceThresholds(lines []LowBalanceThreshold) error {
	if len(lines) > maxLowBalanceThresholds {
		return ErrAlertConfiguration
	}
	seen := map[uuid.UUID]bool{}
	for _, l := range lines {
		_, err := platform.ParseExact(l.Threshold)
		if err != nil || strings.HasPrefix(l.Threshold, "-") || l.SupplierID == uuid.Nil() || seen[l.SupplierID] {
			return ErrAlertConfiguration
		}
		seen[l.SupplierID] = true
	}
	return nil
}

// LowBalanceLines is each supplier's alert line as saved, for suppliers' balance alerts (cmd/axiom wires it).
func LowBalanceLines(ctx context.Context, tx pg.Tx) (map[uuid.UUID]string, error) {
	c, err := readAlertConfiguration(ctx, tx, false)
	if err != nil || c.LowBalanceThresholds == nil {
		return nil, err
	}
	lines := make(map[uuid.UUID]string, len(*c.LowBalanceThresholds))
	for _, l := range *c.LowBalanceThresholds {
		lines[l.SupplierID] = l.Threshold
	}
	return lines, nil
}
