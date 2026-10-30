package observability

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// SpendDetection tunes the offline account detector. Every value is admin data; the zero value is not meaningful, and
// getSpendDetection returns the code defaults until an administrator saves a complete set.
type SpendDetection struct {
	Enabled           bool    `json:"enabled"`
	NotifyUsers       bool    `json:"notify_users"`
	WindowSeconds     int     `json:"window_seconds"`
	BaselineSeconds   int     `json:"baseline_seconds"`
	MinSpendNano      int64   `json:"min_spend_nano"`
	SpendRatio        float64 `json:"spend_ratio"`
	MinNewRegionCalls int     `json:"min_new_region_calls"`
	MinNewModelCalls  int     `json:"min_new_model_calls"`
}

// minSpendWindow is the shortest recent window an administrator may set, and spendEvery is how often the job runs. The
// interval must stay below the window, or a burst that happens between two runs falls outside both recent windows and is
// never detected. The overlap also absorbs a late run.
const (
	minSpendWindow = 15 * time.Minute
	spendEvery     = 10 * time.Minute
)

// The detector compares one recent hour with the account's own preceding day. A shorter pair would react to normal
// bursts, and a longer baseline reads more records without making a stolen key's first hour any more obvious.
//
// NotifyUsers defaults to off: an older node's dispatcher claims any undispatched notice, cannot render a kind it does
// not know, and would mark the reminder sent without sending it. Enable it once every node runs a version that knows
// the kind; detection and the alert channel do not wait for that.
func defaultSpendDetection() SpendDetection {
	return SpendDetection{
		Enabled: true, NotifyUsers: false, WindowSeconds: 3600, BaselineSeconds: 86400, MinSpendNano: 5_000_000_000,
		SpendRatio: 3, MinNewRegionCalls: 5, MinNewModelCalls: 5,
	}
}

func (d SpendDetection) validate() error {
	if !d.validWindow() || !d.validSpend() || !d.validCalls() {
		return ErrAlertConfiguration
	}
	return nil
}

func (d SpendDetection) validWindow() bool {
	return d.WindowSeconds >= int(minSpendWindow/time.Second) && d.WindowSeconds <= 86400 &&
		d.BaselineSeconds >= d.WindowSeconds && d.BaselineSeconds <= 604800
}

func (d SpendDetection) validSpend() bool {
	return d.MinSpendNano >= 0 && d.MinSpendNano <= 1_000_000_000_000_000_000 &&
		d.SpendRatio >= 1 && d.SpendRatio <= 1000
}

func (d SpendDetection) validCalls() bool {
	return d.MinNewRegionCalls >= 1 && d.MinNewRegionCalls <= 1_000_000 &&
		d.MinNewModelCalls >= 1 && d.MinNewModelCalls <= 1_000_000
}

// getSpendDetection returns the saved thresholds, or the code defaults before the first save. They are stored apart
// from the alert rules so an older node replacing the rules cannot drop them.
func getSpendDetection(ctx context.Context, tx pg.Tx) (SpendDetection, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT body FROM observability.spend_detection WHERE singleton`).Scan(&raw)
	if errors.Is(err, pg.ErrNoRows) {
		return defaultSpendDetection(), nil
	}
	if err != nil {
		return SpendDetection{}, err
	}
	var d SpendDetection
	if err = jsonx.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	// The row is a boundary like the request: a value outside the contract would otherwise reach a ratio computation
	// that cannot represent it. The write path already refuses these, so this only reports damage.
	if err = d.validate(); err != nil {
		return d, fmt.Errorf("observability: saved spend detection: %w", err)
	}
	return d, nil
}

const (
	spendAnomalyKind = "observability.spend_anomaly"
	maxInt64         = int64(^uint64(0) >> 1)
)

// anomalyID is one firing account-level anomaly: a spend spike in a currency, a new source network, or an unused model.
type anomalyID struct {
	account uuid.UUID
	kind    string
}

func (id anomalyID) alertKey() string { return id.account.String() + "/" + id.kind }

type spendAnomaly struct {
	fields map[string]string
	value  int64 // recent spend in nano, for the user notice; zero when the anomaly itself is not a spend spike
}

// detectSpendAnomalies compares every account with calls in the recent window against its own baseline, persists one
// signal per new incident and resolves the incidents that are gone. It is one transaction so a notice, its detector
// state and its signal cannot disagree.
func detectSpendAnomalies(ctx context.Context, tx pg.Tx, now time.Time) (fired, resolved int, err error) {
	now = now.UTC()
	d, err := getSpendDetection(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	active, err := readSpendAnomalies(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if !d.Enabled {
		return 0, 0, resolveSpendAnomalies(ctx, tx, active)
	}
	w := newSpendWindow(now, d)
	if w.accounts, err = recentCallAccounts(ctx, tx, w.recentStart, w.now); err != nil {
		return 0, 0, err
	}
	found, err := w.collect(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if fired, err = raiseSpendAnomalies(ctx, tx, found, active, d.NotifyUsers); err != nil {
		return 0, 0, err
	}
	resolved, err = resolveGoneSpendAnomalies(ctx, tx, active, found)
	return fired, resolved, err
}

type spendWindow struct {
	detection                       SpendDetection
	accounts                        []uuid.UUID
	now, recentStart, baselineStart time.Time
}

func newSpendWindow(now time.Time, d SpendDetection) spendWindow {
	return spendWindow{
		detection:     d,
		now:           now,
		recentStart:   now.Add(-time.Duration(d.WindowSeconds) * time.Second),
		baselineStart: now.Add(-time.Duration(d.WindowSeconds+d.BaselineSeconds) * time.Second),
	}
}

func (w spendWindow) collect(ctx context.Context, tx pg.Tx) (map[anomalyID]spendAnomaly, error) {
	found := map[anomalyID]spendAnomaly{}
	if len(w.accounts) == 0 {
		return found, nil
	}
	baselineCalls, err := w.collectSpend(ctx, tx, found)
	if err != nil {
		return nil, err
	}
	if err = w.collectRegions(ctx, tx, found, baselineCalls); err != nil {
		return nil, err
	}
	if err = w.collectModels(ctx, tx, found, baselineCalls); err != nil {
		return nil, err
	}
	return found, nil
}

// recentCallAccounts is the small set the detector has to look at; the time-led index keeps it off the whole monthly
// partition.
func recentCallAccounts(ctx context.Context, tx pg.Tx, since, until time.Time) ([]uuid.UUID, error) {
	// price_nano >= 0 is always true (the table checks it) and names the detector-only partial index, so console
	// queries that filter by account keep their account-led plan.
	rows, err := tx.Query(ctx, `SELECT DISTINCT account_id FROM observability.call_log WHERE at >= $1 AND at < $2 AND price_nano >= 0`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []uuid.UUID
	for rows.Next() {
		var account uuid.UUID
		if err := rows.Scan(&account); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// collectSpend adds one anomaly per currency whose recent spend is above both the absolute floor and the account's
// own rate. It returns every account's baseline call count so the other two signals can require a usable baseline.
func (w spendWindow) collectSpend(ctx context.Context, tx pg.Tx, found map[anomalyID]spendAnomaly) (map[uuid.UUID]int64, error) {
	// The sums are numeric text: an account can spend more than int64 in a day, and the detector must report that
	// instead of failing the whole run on an overflow. The rule's value is clamped, since its threshold is a float.
	rows, err := tx.Query(ctx, `SELECT account_id,currency,
 coalesce(sum(price_nano) FILTER (WHERE at >= $2),0)::text,
 count(*) FILTER (WHERE at >= $2),
 coalesce(sum(price_nano) FILTER (WHERE at < $2),0)::text,
 count(*) FILTER (WHERE at < $2)
 FROM observability.call_log
 WHERE account_id = ANY($1) AND at >= $3 AND at < $4
 GROUP BY account_id,currency`, w.accounts, w.recentStart, w.baselineStart, w.now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	baseCalls := map[uuid.UUID]int64{}
	for rows.Next() {
		var account uuid.UUID
		var currency, recentText, baselineText string
		var recentCalls, baselineCalls int64
		if err := rows.Scan(&account, &currency, &recentText, &recentCalls, &baselineText, &baselineCalls); err != nil {
			return nil, err
		}
		baseCalls[account] += baselineCalls
		a, ok, err := spendAnomalyFor(account, currency, recentText, baselineText, recentCalls, baselineCalls, w.detection)
		if err != nil {
			return nil, err
		}
		if ok {
			found[anomalyID{account, "spend/" + currency}] = a
		}
	}
	return baseCalls, rows.Err()
}

// spendAnomalyFor applies the floor and the ratio to one currency. A currency the account never used has no rate to
// compare against, so it is not flagged by itself; the other two signals still cover a new destination.
func spendAnomalyFor(account uuid.UUID, currency, recentText, baselineText string, recentCalls, baselineCalls int64, d SpendDetection) (spendAnomaly, bool, error) {
	recent, ok := new(big.Int).SetString(recentText, 10)
	if !ok {
		return spendAnomaly{}, false, fmt.Errorf("observability: spend sum %q", recentText)
	}
	baseline, ok := new(big.Int).SetString(baselineText, 10)
	if !ok {
		return spendAnomaly{}, false, fmt.Errorf("observability: baseline sum %q", baselineText)
	}
	if baselineCalls == 0 || recent.Cmp(big.NewInt(d.MinSpendNano)) < 0 {
		return spendAnomaly{}, false, nil
	}
	expected := new(big.Rat).SetFrac(
		new(big.Int).Mul(baseline, big.NewInt(int64(d.WindowSeconds))),
		big.NewInt(int64(d.BaselineSeconds)))
	if expected.Sign() > 0 && new(big.Rat).SetInt(recent).Cmp(new(big.Rat).Mul(new(big.Rat).SetFloat64(d.SpendRatio), expected)) < 0 {
		return spendAnomaly{}, false, nil
	}
	ratio := "new"
	if expected.Sign() > 0 {
		ratio = new(big.Rat).Quo(new(big.Rat).SetInt(recent), expected).FloatString(2)
	}
	alertValue := recent.String()
	var noticeValue int64
	if recent.IsInt64() {
		noticeValue = recent.Int64()
	} else {
		alertValue = strconv.FormatInt(maxInt64, 10)
	}
	return spendAnomaly{value: noticeValue, fields: map[string]string{
		"account":          account.String(),
		"value":            alertValue,
		"currency":         currency,
		"recent_nano":      recent.String(),
		"baseline_nano":    baseline.String(),
		"baseline_calls":   strconv.FormatInt(baselineCalls, 10),
		"recent_calls":     strconv.FormatInt(recentCalls, 10),
		"ratio":            ratio,
		"window_seconds":   strconv.Itoa(d.WindowSeconds),
		"baseline_seconds": strconv.Itoa(d.BaselineSeconds),
	}}, true, nil
}

type callCount struct{ recent, baseline int64 }

// callCounts groups recent and baseline calls by an account and one text dimension.
func callCounts(ctx context.Context, tx pg.Tx, query string, args ...any) (map[uuid.UUID]map[string]callCount, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byAccount := map[uuid.UUID]map[string]callCount{}
	for rows.Next() {
		var account uuid.UUID
		var value string
		var recent, baseline int64
		if err := rows.Scan(&account, &value, &recent, &baseline); err != nil {
			return nil, err
		}
		if byAccount[account] == nil {
			byAccount[account] = map[string]callCount{}
		}
		byAccount[account][value] = callCount{recent, baseline}
	}
	return byAccount, rows.Err()
}

// collectRegions flags networks absent from the account's baseline. Without a geolocation source the network allocation
// is the closest stable stand-in for a region: /16 for IPv4 and /32 for IPv6, one hop wider than the gateway's groups so
// a customer's own addresses do not look foreign to each other.
func (w spendWindow) collectRegions(ctx context.Context, tx pg.Tx, found map[anomalyID]spendAnomaly, baselineCalls map[uuid.UUID]int64) error {
	counts, err := callCounts(ctx, tx, `SELECT account_id,network,
 count(*) FILTER (WHERE at >= $2),
 count(*) FILTER (WHERE at < $2)
 FROM (
  SELECT account_id,at,network(CASE WHEN family(ip)=4 THEN set_masklen(ip,16) ELSE set_masklen(ip,32) END)::text AS network
  FROM observability.call_log
  WHERE account_id = ANY($1) AND at >= $3 AND at < $4
 ) calls GROUP BY account_id,network`, w.accounts, w.recentStart, w.baselineStart, w.now)
	if err != nil {
		return err
	}
	addNewValues(found, counts, baselineCalls, "region", "networks", w.detection.MinNewRegionCalls)
	return nil
}

// collectModels flags models the account had not called in its baseline.
func (w spendWindow) collectModels(ctx context.Context, tx pg.Tx, found map[anomalyID]spendAnomaly, baselineCalls map[uuid.UUID]int64) error {
	counts, err := callCounts(ctx, tx, `SELECT account_id,model,
 count(*) FILTER (WHERE at >= $2),
 count(*) FILTER (WHERE at < $2)
 FROM observability.call_log
 WHERE account_id = ANY($1) AND at >= $3 AND at < $4
 GROUP BY account_id,model`, w.accounts, w.recentStart, w.baselineStart, w.now)
	if err != nil {
		return err
	}
	addNewValues(found, counts, baselineCalls, "model", "models", w.detection.MinNewModelCalls)
	return nil
}

func addNewValues(found map[anomalyID]spendAnomaly, counts map[uuid.UUID]map[string]callCount, baselineCalls map[uuid.UUID]int64, kind, field string, minimum int) {
	for account, values := range counts {
		if baselineCalls[account] == 0 {
			continue
		}
		var names []string
		var calls int64
		for value, s := range values {
			if s.baseline == 0 && s.recent >= int64(minimum) {
				names = append(names, value)
				calls += s.recent
			}
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		found[anomalyID{account, kind}] = spendAnomaly{fields: map[string]string{
			"account":   account.String(),
			"value":     strconv.FormatInt(calls, 10),
			field:       boundedList(names, 8),
			"calls":     strconv.FormatInt(calls, 10),
			"threshold": strconv.Itoa(minimum),
		}}
	}
}

// boundedList keeps the event fields inside the signal's 512-byte value limit and says how many were left out.
func boundedList(items []string, max int) string {
	var b strings.Builder
	n := 0
	for _, item := range items {
		if n == max || b.Len()+len(item)+1 > 480 {
			break
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(item)
		n++
	}
	if n < len(items) {
		fmt.Fprintf(&b, ",+%d", len(items)-n)
	}
	return b.String()
}

// raiseSpendAnomalies inserts the incident state, its durable signal and the user notice together. RowsAffected==0
// means the incident was already firing, so a persistent anomaly is noticed once; an account that already has any
// incident is not reminded again just because a second kind appears in the same burst.
func raiseSpendAnomalies(ctx context.Context, tx pg.Tx, found map[anomalyID]spendAnomaly, active map[anomalyID]struct{}, notify bool) (int, error) {
	notices := map[uuid.UUID]accounts.SpendAnomalyNotice{}
	notified := map[uuid.UUID]bool{}
	for id := range active {
		notified[id.account] = true
	}
	fired := 0
	for _, id := range sortedSpendAnomalies(found) {
		a := found[id]
		tag, err := tx.Exec(ctx, `INSERT INTO observability.spend_anomalies(account_id,kind) VALUES($1,$2) ON CONFLICT DO NOTHING`, id.account, id.kind)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: spendAnomalyKind, Key: id.alertKey(), Severity: "warning", Fields: a.fields}); err != nil {
			return 0, err
		}
		// One burst is one user notice even when it trips all three signals or a later kind appears; a later, separate
		// burst has no active state and notifies again. Keep the largest spend across currencies so the amount is not
		// lost when a region or model signal is sorted first.
		addSpendNotice(notices, notified, id, a)
		fired++
	}
	if !notify {
		return fired, nil
	}
	return fired, recordSpendNotices(ctx, tx, notices)
}

// addSpendNotice keeps one notice per account per burst, with the largest spend across currencies so the amount is not
// lost when a region or model signal is visited first.
func addSpendNotice(notices map[uuid.UUID]accounts.SpendAnomalyNotice, notified map[uuid.UUID]bool, id anomalyID, a spendAnomaly) {
	if n, ok := notices[id.account]; ok {
		if a.value > n.Value {
			n.Value = a.value
			notices[id.account] = n
		}
		return
	}
	if notified[id.account] {
		return
	}
	notices[id.account] = accounts.SpendAnomalyNotice{Account: id.account, Source: uuid.NewV7().String(), Value: a.value}
	notified[id.account] = true
}

func recordSpendNotices(ctx context.Context, tx pg.Tx, notices map[uuid.UUID]accounts.SpendAnomalyNotice) error {
	list := make([]accounts.SpendAnomalyNotice, 0, len(notices))
	for _, n := range notices {
		list = append(list, n)
	}
	slices.SortFunc(list, func(a, b accounts.SpendAnomalyNotice) int { return cmp.Compare(a.Account.String(), b.Account.String()) })
	return accounts.RecordSpendAnomalyNotices(ctx, tx, list)
}

func readSpendAnomalies(ctx context.Context, tx pg.Tx) (map[anomalyID]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT account_id,kind FROM observability.spend_anomalies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	active := map[anomalyID]struct{}{}
	for rows.Next() {
		var id anomalyID
		if err := rows.Scan(&id.account, &id.kind); err != nil {
			return nil, err
		}
		active[id] = struct{}{}
	}
	return active, rows.Err()
}

func resolveGoneSpendAnomalies(ctx context.Context, tx pg.Tx, active map[anomalyID]struct{}, found map[anomalyID]spendAnomaly) (int, error) {
	var gone []anomalyID
	for _, id := range sortedSpendAnomalies(active) {
		if _, ok := found[id]; !ok {
			gone = append(gone, id)
		}
	}
	return resolveSpendIDs(ctx, tx, gone)
}

// resolveSpendAnomalies closes every incident when an administrator disables the detector; leaving them firing would
// page on a signal nobody is watching any more.
func resolveSpendAnomalies(ctx context.Context, tx pg.Tx, active map[anomalyID]struct{}) error {
	_, err := resolveSpendIDs(ctx, tx, sortedSpendAnomalies(active))
	return err
}

func resolveSpendIDs(ctx context.Context, tx pg.Tx, ids []anomalyID) (int, error) {
	resolved := 0
	for _, id := range ids {
		tag, err := tx.Exec(ctx, `DELETE FROM observability.spend_anomalies WHERE account_id=$1 AND kind=$2`, id.account, id.kind)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: spendAnomalyKind, Key: id.alertKey(), Severity: "info", Resolved: true}); err != nil {
			return 0, err
		}
		resolved++
	}
	return resolved, nil
}

func sortedSpendAnomalies[M ~map[anomalyID]V, V any](m M) []anomalyID {
	ids := make([]anomalyID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].account != ids[j].account {
			return ids[i].account.String() < ids[j].account.String()
		}
		return ids[i].kind < ids[j].kind
	})
	return ids
}
