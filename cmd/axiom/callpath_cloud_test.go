package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers"
)

// The sale and official prices of the recorded Chat sample's 9 input and 12 output tokens (withChatUsage), in
// billionths of a dollar: one and a half times, and once, servedReference's three and fifteen dollars a million.
const (
	cloudChatCharge = 9*4_500 + 12*22_500
	cloudChatCost   = 9*3_000 + 12*15_000
)

// TestCloudSuppliersServeAtTheOfficialPrice: a cloud platform connected with its credential serves a call with no
// balance to read, signed its own way, and the call costs the model's official price, as a direct API's does (the
// supplier's cost row at a unit value of 1, with no request id), while the client pays the sale price. Vertex AI's key
// is exchanged for a token and its Messages go to the model's streamRawPredict in the region; Bedrock's are signed and
// streamed in its event-stream framing, which the gateway reads as Anthropic's SSE; Azure OpenAI's Chat goes to the
// deployment with the resource's key. A Bedrock key the platform stops accepting is marked rejected from the call.
func TestCloudSuppliersServeAtTheOfficialPrice(t *testing.T) {
	t.Parallel()
	messages, err := os.ReadFile(filepath.Join("..", "..", "internal", "protocol", "testdata", "messages", "text", "upstream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	chat, err := os.ReadFile(filepath.Join("..", "..", "internal", "protocol", "testdata", "chat", "text", "upstream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []cloudCall{
		{
			"vertex",
			suppliers.CloudRequest{Kind: "vertex", Input: suppliers.CloudInput{ServiceAccountJSON: serviceAccountKey()}, Regions: []string{"us-east5"}},
			"/v1/messages", messagesCall, "us-east5-aiplatform.googleapis.com/v1/projects/harbor-prod/locations/us-east5/publishers/anthropic/models/claude-opus-5-5@20261101:streamRawPredict",
			"Bearer " + cloudGrant, messagesCharge, directCost, "0.0003", false,
		},
		{
			"bedrock",
			suppliers.CloudRequest{Kind: "bedrock", Input: suppliers.CloudInput{AccessKeyID: cloudKeyID, SecretAccessKey: "fake/secret+key"}, Regions: []string{"us-east-1"}},
			"/v1/messages", messagesCall, "bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-opus-5-5-v1%3A0/invoke-with-response-stream",
			"AWS4-HMAC-SHA256 Credential=" + cloudKeyID + "/", messagesCharge, directCost, "0.0003", true,
		},
		{
			"azure",
			suppliers.CloudRequest{
				Kind: "azure", Origin: "https://harbor.openai.azure.com", Input: suppliers.CloudInput{APIKey: cloudResourceKey},
				Deployments: []suppliers.Deployment{{Name: "prod-astra", Model: "gpt-6-astra"}},
			},
			"/v1/chat/completions", `{"model":"gpt-6-astra","messages":[{"role":"user","content":"Hello"}]}`,
			"harbor.openai.azure.com/openai/deployments/prod-astra/chat/completions", cloudResourceKey, cloudChatCharge, cloudChatCost, "0.000207", false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.serve(t, newFakeCloud(t, string(messages), withChatUsage(string(chat))))
		})
	}
}

// cloudCall is one platform's call: how it is connected, the call a client makes, where and how the platform is sent
// it, what it charges and costs, and whether a refused key is then marked rejected.
type cloudCall struct {
	name              string
	request           suppliers.CloudRequest
	path, body        string
	sentTo, auth      string
	charge, cost      int64
	estimated         string
	rejectedOnRefusal bool
}

// serve connects the platform at the fake in a world whose relay is paused, makes the call and checks what it cost.
func (tc cloudCall) serve(t *testing.T, fake *fakeCloud) {
	t.Helper()
	w := newCallWorld(t)
	if err := pgtest.Run(t.Context(), w.database.Superuser, "UPDATE suppliers.suppliers SET status = 'paused'"); err != nil {
		t.Fatal(err)
	}
	w.trusted, w.routes = append(w.trusted, fake.cert), fake.routes(t)
	if tc.name == "vertex" {
		// Vertex AI names a fixed model by its dated name, which the catalog has for a model of the vendor's.
		w.addPublicName("anthropic/claude-opus-5-5", "claude-opus-5-5-20261101")
	}
	supplier := w.connectCloud(fake, tc.request)
	w.price()
	n := w.start()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })
	r := w.call(n, tc.path, w.richKey, tc.body)
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	sent := fake.sent()
	if call := sent[len(sent)-1]; call.path != tc.sentTo || !strings.HasPrefix(call.auth, tc.auth) {
		t.Errorf("the platform was sent %s with %q, want %s with %s", call.path, call.auth, tc.sentTo, tc.auth)
	}
	tc.checkCost(w, r, supplier)
	if tc.rejectedOnRefusal {
		fake.refuse.Store(true)
		w.call(n, tc.path, w.richKey, tc.body)
		waitUntil(t, 10*time.Second, func() bool {
			return w.query(`SELECT string_agg(status, ',') FROM suppliers.credentials WHERE supplier_id::text = $1`, supplier) == "rejected"
		})
	}
}

// checkCost: the client paid the sale price, and the supplier's cost row and the call log hold the official price.
func (tc cloudCall) checkCost(w *callWorld, r reply, supplier string) {
	w.t.Helper()
	id := r.header.Get("X-Generation-Id")
	w.checkSettled(r, tc.charge)
	w.checkBalanced()
	if got := w.query(`SELECT supplier_id::text || ' ' || estimated_native::text || ' ' || cost_flag || ' ' || coalesce(charge_ref, '-')
		FROM suppliers.attempt_costs WHERE request_id::text = $1`, id); got != supplier+" "+tc.estimated+" complete -" {
		w.t.Errorf("the supplier's cost row is %q, want the platform's at the official price, with no request id", got)
	}
	var logged string
	waitUntil(w.t, 5*time.Second, func() bool {
		logged = w.query(`SELECT coalesce(max(price_nano)::text || ' ' || max(cost_nano)::text, '') FROM observability.call_log WHERE id::text = $1`, id)
		return logged != ""
	})
	if want := strconv.FormatInt(tc.charge, 10) + " " + strconv.FormatInt(tc.cost, 10); logged != want {
		w.t.Errorf("the call log holds %q, want the sale price and the official price %q", logged, want)
	}
}

// addPublicName gives the model of the slug one more public name.
func (w *callWorld) addPublicName(slug, name string) {
	w.t.Helper()
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	op := catalog.Op{Actor: audit.System("callpath.test"), Key: uuid.NewV7(), Reason: "test"}
	if _, err := cat.AddName(w.t.Context(), catalog.AddNameRequest{
		Op: op, Model: catalog.ModelID(w.models[slug]), Name: name, Visibility: catalog.VisibilityPublic,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// connectCloud connects the platform at the fake with the request's credential, through the suppliers module as the
// admin handler does, and returns the supplier's ID.
func (w *callWorld) connectCloud(fake *fakeCloud, r suppliers.CloudRequest) string {
	w.t.Helper()
	ctx := w.t.Context()
	admin, snap := w.cloudAdmin(fake)
	plan := w.checkedCloud(admin, r)
	var made suppliers.Connected
	if err := w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
		made, err = admin.CreateCloud(ctx, tx, audit.System("callpath.test"), snap, plan)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	if made.Supplier == nil {
		w.t.Fatalf("the credential did not pass: %+v", made.Checks)
	}
	return made.Supplier.ID.String()
}

// cloudAdmin is the suppliers module's admin side reaching the fake, and the world's catalog as it is now.
func (w *callWorld) cloudAdmin(fake *fakeCloud) (*suppliers.Admin, *catalog.Snapshot) {
	w.t.Helper()
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := cat.Refresh(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	snap, err := cat.Current()
	if err != nil {
		w.t.Fatal(err)
	}
	admin, err := suppliers.NewAdmin(suppliers.AdminConfig{Keys: masterKeys(w.t), Egress: fake.egress(w.t)})
	if err != nil {
		w.t.Fatal(err)
	}
	return admin, snap
}

// checkedCloud is the request planned and checked at the fake, as the admin handler does before it stores anything.
func (w *callWorld) checkedCloud(admin *suppliers.Admin, r suppliers.CloudRequest) *suppliers.CloudPlan {
	w.t.Helper()
	ctx := w.t.Context()
	r.Name, r.Limits, r.Reason = "Cloud", suppliers.Limits{MaxConcurrency: 4}, "a cloud source"
	var plan *suppliers.CloudPlan
	if err := w.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
		plan, err = admin.PlanCloud(ctx, tx, r)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	if err := admin.CheckCloud(ctx, plan); err != nil {
		w.t.Fatal(err)
	}
	return plan
}

// withChatUsage is the Chat sample with the usage chunk that stream_options.include_usage asks for, which the sample
// leaves out.
func withChatUsage(stream string) string {
	const usage = `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-6-astra","choices":[],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}` + "\n\n"
	return strings.Replace(stream, "data: [DONE]", usage+"data: [DONE]", 1)
}
