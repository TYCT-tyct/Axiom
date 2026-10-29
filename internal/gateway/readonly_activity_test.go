package gateway

import (
	"net/http"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// activityRows decodes /activity's data array.
func activityRows(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := jsonx.Unmarshal([]byte(body), &out); err != nil || out.Data == nil {
		t.Fatalf("not a data array: %s (%v)", body, err)
	}
	return out.Data
}

// TestActivityGroupsTheLastCompletedDays: the account's calls are summed over UTC day and model, with the sale charge
// and the token counts, optionally split by workspace; today is not a completed day and is not included.
func TestActivityGroupsTheLastCompletedDays(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for range 2 {
		id := uuid.NewV7()
		r.callRow(a, id)
		r.sql("UPDATE observability.call_log SET at = now() - interval '1 day' WHERE id = '" + id.String() + "'")
	}
	today := uuid.NewV7()
	r.callRow(a, today) // outside the completed days
	for _, query := range []string{"", "?group_by=workspace", "?api_key_hash=" + uuid.UUID(a.keyID).String()} {
		checkActivityGroup(t, r, a, query)
	}
	checkActivityDate(t, r, a)
	checkActivityRefusals(t, r, a)
}

// checkActivityGroup checks the single group one query returns: its identity and the summed counts.
func checkActivityGroup(t *testing.T, r *rig, a account, query string) {
	t.Helper()
	resp, body := r.do(http.MethodGet, "/api/v1/activity"+query, a.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", query, resp.StatusCode, body)
	}
	rows := activityRows(t, body)
	if len(rows) != 1 {
		t.Fatalf("%s: %d rows: %s", query, len(rows), body)
	}
	row := rows[0]
	if row["model"] != "acme/alpha" || row["provider_name"] != "Axiom" || row["endpoint_id"] != uuid.Nil().String() {
		t.Errorf("%s: identity: %v", query, row)
	}
	for field, want := range map[string]float64{
		"requests": 2, "prompt_tokens": 280, "completion_tokens": 50, "reasoning_tokens": 10,
		"cached_tokens": 80, "usage": 3, "byok_usage_inference": 0,
	} {
		if !near(row[field], want) {
			t.Errorf("%s: %s = %v, want %v", query, field, row[field], want)
		}
	}
	if query == "?group_by=workspace" {
		if row["workspace_id"] != a.workspace.String() {
			t.Errorf("workspace: %v", row["workspace_id"])
		}
	} else if _, ok := row["workspace_id"]; ok {
		t.Errorf("workspace shown without group_by: %v", row)
	}
}

// checkActivityDate asks for the day before today, the newest completed day, and gets its one group.
func checkActivityDate(t *testing.T, r *rig, a account) {
	t.Helper()
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if resp, body := r.do(http.MethodGet, "/api/v1/activity?date="+day, a.key, nil); resp.StatusCode != http.StatusOK ||
		len(activityRows(t, body)) != 1 {
		t.Errorf("date %s: %d %s", day, resp.StatusCode, body)
	}
}

// checkActivityRefusals checks the parameters OpenRouter's activity cannot honour.
func checkActivityRefusals(t *testing.T, r *rig, a account) {
	t.Helper()
	for _, query := range []string{"?date=2020-01-01", "?date=today", "?group_by=model", "?user_id=u"} {
		resp, body := r.do(http.MethodGet, "/api/v1/activity"+query, a.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", query, resp.StatusCode, body)
		}
	}
}

// TestActivityUsesTheCurrentCharge: /activity must not sum the immutable call-log snapshot after a correction moved
// the reservation and ledger. The current settled charge wins; a call with no reservation keeps its snapshot.
func TestActivityUsesTheCurrentCharge(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	id, id2 := uuid.NewV7(), uuid.NewV7()
	r.callRow(a, id)
	r.callRow(a, id2)
	r.sql(
		"UPDATE observability.call_log SET at = now() - interval '1 day', price_nano = 100000000 WHERE id IN ('"+id.String()+"', '"+id2.String()+"')",
		"INSERT INTO billing.holders (id, expires_at) VALUES ('"+uuid.NewV7().String()+"', now() + interval '1 day')",
	)
	var holder uuid.UUID
	if err := r.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT id FROM billing.holders ORDER BY started_at DESC LIMIT 1").Scan(&holder)
	}); err != nil {
		t.Fatal(err)
	}
	for request, charged := range map[uuid.UUID]int64{id: 50000000, id2: 70000000} {
		r.sql("INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id, " +
			"workspace_window, key_window, segment, amount, state, deadline, charged, price) VALUES ('" + request.String() + "', '" +
			siteID.String() + "', '" + a.id.String() + "', '" + a.workspace.String() + "', '" + uuid.UUID(a.keyID).String() +
			"', '" + holder.String() + "', now(), now(), 1, 100000000, 'settled', now() + interval '1 hour', " +
			strconv.FormatInt(charged, 10) + ", 100000000)")
	}
	resp, body := r.do(http.MethodGet, "/api/v1/activity", a.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	rows := activityRows(t, body)
	if len(rows) != 1 || rows[0]["requests"] != float64(2) || !near(rows[0]["usage"], 0.12) {
		t.Fatalf("activity %v, want two requests and usage 0.12 from both corrected reservations", rows)
	}
}

// TestActivityUsesThePhysicalPrompt: cache reads and writes may overlap, so adding the billed input items overcounts.
// The stored physical prompt is the client's total and wins when present.
func TestActivityUsesThePhysicalPrompt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	id := uuid.NewV7()
	r.callRow(a, id)
	r.sql("UPDATE observability.call_log SET at = now() - interval '1 day', physical_input = 100, " +
		"usage = '{\"input\":0,\"cache_read\":80,\"cache_write\":60,\"output\":20}' WHERE id = '" + id.String() + "'")
	resp, body := r.do(http.MethodGet, "/api/v1/activity", a.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	rows := activityRows(t, body)
	if len(rows) != 1 || !near(rows[0]["prompt_tokens"], 100) {
		t.Fatalf("activity %v, want prompt_tokens 100 from physical_input", rows)
	}
}
