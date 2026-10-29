package main

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pgtest"
)

func TestCallChargeDetailsAcrossAPIs(t *testing.T) {
	w := newCallWorld(t)
	w.price()
	n := w.start()
	defer func() {
		if err := n.stop(); err != nil {
			t.Error(err)
		}
	}()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })
	response := w.call(n, "/v1/messages", w.richKey, messagesCall)
	if response.status != http.StatusOK {
		t.Fatalf("call: %d %s", response.status, response.body)
	}
	id := response.header.Get("X-Generation-Id")
	waitUntil(t, 5*time.Second, func() bool {
		return w.query("SELECT count(*)::text FROM observability.call_log WHERE id::text=$1", id) == "1"
	})
	generation := generationCharges(t, n, w.richKey, id)
	f := startNode(w.commandLine, &testClock{now: time.Now()}, 0)
	session := f.signIn("rich@example.com", ownerPassword)
	checkConsoleCharges(t, f, session, id, generation)
	checkUnavailableChargeAPIs(t, w, n, f, session, id)
}

func generationCharges(t *testing.T, n runningNode, key, id string) billing.ChargeDetails {
	t.Helper()
	body := generationBody(t, n, key, id)
	var got struct {
		Data struct {
			Details *billing.ChargeDetails `json:"charge_details"`
			Version string                 `json:"price_version"`
		} `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Data.Details == nil || got.Data.Version == "" {
		t.Fatalf("generation: %s", body)
	}
	checkUserChargeFields(t, body)
	for _, item := range got.Data.Details.Items {
		if item.PriceVersion != got.Data.Version {
			t.Errorf("item version: %+v / %s", item, got.Data.Version)
		}
	}
	return *got.Data.Details
}

func checkConsoleCharges(t *testing.T, f consoleFixture, s session, id string, want billing.ChargeDetails) {
	t.Helper()
	var call consoleapi.Call
	rec := f.do(http.MethodGet, "/calls/"+id, "", as(s))
	expect(t, "getCall charges", rec, http.StatusOK, "")
	decode(t, rec, &call)
	checkChargeResponse(t, call, want)
	checkUserChargeFields(t, rec.Body.Bytes())
	var list consoleapi.CallList
	rec = f.do(http.MethodGet, "/calls", "", as(s))
	expect(t, "listCalls charges", rec, http.StatusOK, "")
	decode(t, rec, &list)
	if len(list.Data) != 1 {
		t.Fatalf("list: %+v", list)
	}
	checkChargeResponse(t, list.Data[0], want)
	checkUserChargeFields(t, rec.Body.Bytes())
}

func checkChargeResponse(t *testing.T, call consoleapi.Call, want billing.ChargeDetails) {
	t.Helper()
	gotRaw, err := jsonx.MarshalCanonical(call.ChargeDetails)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, err := jsonx.MarshalCanonical(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Errorf("console details %s, generation %s", gotRaw, wantRaw)
	}
	amount, err := platform.ParseExact(want.Charged)
	if err != nil {
		t.Fatal(err)
	}
	if call.Price.Amount != strconv.FormatInt(amount.CeilAmount(), 10) {
		t.Errorf("invoice disagrees with call price: %+v", call)
	}
}

func checkUserChargeFields(t *testing.T, body []byte) {
	t.Helper()
	for _, field := range []string{"cost_nano", "supplier_id", "upstream_inference_cost", "endpoint_id", "\"route\"", "\"basis\""} {
		if strings.Contains(string(body), field) {
			t.Errorf("user response leaked %s: %s", field, body)
		}
	}
}

func generationBody(t *testing.T, n runningNode, key, id string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+n.Public+"/api/v1/generation?id="+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "api.main.example"
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generation: %d %s", resp.StatusCode, body)
	}
	return body
}

func checkUnavailableChargeAPIs(t *testing.T, w *callWorld, n runningNode, f consoleFixture, s session, bad string) {
	t.Helper()
	response := w.call(n, "/v1/messages", w.richKey, messagesCall)
	if response.status != http.StatusOK {
		t.Fatalf("second call: %d %s", response.status, response.body)
	}
	good := response.header.Get("X-Generation-Id")
	waitUntil(t, 5*time.Second, func() bool {
		return w.query("SELECT count(*)::text FROM observability.call_log WHERE id::text=$1", good) == "1"
	})
	// A future writer's persisted JSON cannot be produced through this binary's immutable settlement API.
	mutation := fmt.Sprintf(`UPDATE billing.reservations SET final_usage='[{"future_usage":true}]'::jsonb WHERE request_id='%s'`, uuid.MustParse(bad).String())
	if err := pgtest.Run(t.Context(), w.database.Superuser, mutation); err != nil {
		t.Fatal(err)
	}
	body := generationBody(t, n, w.richKey, bad)
	var generation struct {
		Data map[string]any `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &generation); err != nil {
		t.Fatal(err)
	}
	if _, ok := generation.Data["charge_details"]; ok {
		t.Fatalf("incompatible generation details exposed: %s", body)
	}
	checkUnavailableConsoleCalls(t, f, s, bad, good)
}

func checkUnavailableConsoleCalls(t *testing.T, f consoleFixture, s session, bad, good string) {
	t.Helper()
	var one consoleapi.Call
	rec := f.do(http.MethodGet, "/calls/"+bad, "", as(s))
	expect(t, "getCall without details", rec, http.StatusOK, "")
	decode(t, rec, &one)
	if one.ChargeDetails != nil || one.Price.Amount == "0" {
		t.Fatalf("bad optional details affected call: %+v", one)
	}
	var list consoleapi.CallList
	rec = f.do(http.MethodGet, "/calls", "", as(s))
	expect(t, "listCalls with incompatible details", rec, http.StatusOK, "")
	decode(t, rec, &list)
	if len(list.Data) != 2 {
		t.Fatalf("calls lost: %+v", list)
	}
	for _, call := range list.Data {
		if (call.Id == bad && call.ChargeDetails != nil) || (call.Id == good && call.ChargeDetails == nil) {
			t.Errorf("optional details: %+v", call)
		}
	}
	if !strings.Contains(f.logs.String(), bad) {
		t.Errorf("console reconstruction failures were not logged: %s", f.logs.String())
	}
}
