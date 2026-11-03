package gateway

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/alloctest"

	"axiom/internal/protocol"
)

// TestAParseIsRefusedBeforeTheBackend: the parser's work happens before the reservation, so the key is checked before
// the document is sent anywhere. A key whose address is not allowed gets the refusal it would get at admission, and
// the backend is never called.
func TestAParseIsRefusedBeforeTheBackend(t *testing.T) {
	t.Parallel()
	parser := &recordingParser{text: "The parsed page."}
	r := newRig(t, func(o *rigOptions) {
		o.spec, o.parser = parseSpec, parser
		o.price = parsePrice
	})
	a := r.newAccount(1e9)
	r.sql("UPDATE accounts.api_keys SET ip_allow = '{10.0.0.0/8}' WHERE id = '" + uuid.UUID(a.keyID).String() + "'")
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, bytes.NewReader(fileBody("cloudflare-ai")))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s, want 401", resp.StatusCode, out)
	}
	if len(parser.docs) != 0 {
		t.Fatalf("the backend was asked to parse %d documents, want none", len(parser.docs))
	}
	if n := r.count("SELECT count(*) FROM billing.reservations"); n != 0 {
		t.Fatalf("the refusal reserved %d calls", n)
	}
}

// TestAParseRefusedForBudgetAllocatesNothing: the rewrite's work set is charged before it runs, so a request the node
// cannot hold is refused without building the body it would have thrown away. The request replays a megabyte of parsed
// text beside a small file: the rewrite alone would copy it twice before a check that came after the fact.
func TestAParseRefusedForBudgetAllocatesNothing(t *testing.T) {
	t.Parallel()
	parser := &recordingParser{text: "unused"}
	h, c := parseHarness(t, parser)
	h.Memory = protocol.NewMemory(1 << 20) // less than the rewrite's work set, which is the text cap alone
	data := []byte("%PDF-1.7 a scan")
	annotation := `{"type":"file","file":{"hash":"` + fileHash(data) + `","content":[{"type":"text","text":"` +
		strings.Repeat("x", 1<<20) + `"}]}}`
	body := []byte(`{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"cloudflare-ai"}}],` +
		`"messages":[{"role":"assistant","content":"ok","annotations":[` + annotation + `]},` +
		`{"role":"user","content":[{"type":"file","file":{"file_data":"` +
		base64.StdEncoding.EncodeToString(data) + `"}}]}]}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, _, err := h.parseFiles(ctx, c, body, req, true); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("parseFiles() error = %v, want %v", err, ErrOverloaded)
	}
	got := alloctest.Measure(t, alloctest.Function{Name: "refused", F: func() {
		_, _, refusal := h.parseFiles(ctx, c, body, req, true)
		_ = refusal
	}})
	if bytes := got["refused"].Bytes; bytes > 64<<10 {
		t.Fatalf("the refusal allocated %v bytes, want the work-set check only", bytes)
	}
	if !h.Memory.Charge(1 << 20) {
		t.Fatal("the refusal kept the work set it could not hold")
	}
}
