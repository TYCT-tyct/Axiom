package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/consoleapi"
)

// keyView is the parts of a key a test compares.
type keyView struct {
	Name, Prefix string
	Disabled     bool
	Limit        string // the limit's amount, empty for none
	Remaining    string
	Reset        string
	Models, IPs  []string
	Expires      time.Time
}

func viewOf(k consoleapi.Key) keyView {
	v := keyView{Name: k.Name, Prefix: k.Prefix, Disabled: k.Disabled}
	if k.SpendLimit != nil {
		v.Limit = k.SpendLimit.Amount
	}
	if k.LimitRemaining != nil {
		v.Remaining = k.LimitRemaining.Amount
	}
	if k.LimitReset != nil {
		v.Reset = string(*k.LimitReset)
	}
	if k.Models != nil {
		v.Models = *k.Models
	}
	if k.IpAllow != nil {
		v.IPs = *k.IpAllow
	}
	if k.ExpiresAt != nil {
		v.Expires = *k.ExpiresAt
	}
	return v
}

func checkKey(t *testing.T, what string, got consoleapi.Key, want keyView) {
	t.Helper()
	if diff := cmp.Diff(want, viewOf(got)); diff != "" {
		t.Errorf("%s (-want +got):\n%s", what, diff)
	}
}

// createKey makes a key from body and returns the response's key and secret.
func (f consoleFixture) createKey(s session, body string) consoleapi.CreatedKey {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/keys", body, as(s))
	expect(f.t, "createKey "+body, rec, http.StatusCreated, "")
	var created consoleapi.CreatedKey
	decode(f.t, rec, &created)
	return created
}

// TestCreateKey: a key without settings gets the site's daily limit for new keys; its text is in the response only,
// begins with the site's prefix, and ends with the last four characters the key shows, in the account's default
// workspace.
func TestCreateKey(t *testing.T) {
	f := newConsoleFixture(t)
	plain := f.createKey(f.owner(), `{"name":"Plain"}`)
	checkKey(t, "a key without settings", plain.Key, keyView{
		Name: "Plain", Prefix: "sk-ax-v1-", Limit: "10000000000", Remaining: "10000000000", Reset: "daily",
	})
	if !strings.HasPrefix(plain.Secret, "sk-ax-v1-") || !strings.HasSuffix(plain.Secret, plain.Key.Last4) || len(plain.Secret) != len("sk-ax-v1-")+49 {
		t.Errorf("the key's text %q and last four %q", plain.Secret, plain.Key.Last4)
	}
	if !strings.HasPrefix(plain.Key.WorkspaceId, "ws_") || time.Since(plain.Key.CreatedAt) > time.Minute {
		t.Errorf("workspace %q, created %v", plain.Key.WorkspaceId, plain.Key.CreatedAt)
	}
}

// TestCreateKeyWithSettings: settings are kept as sent: limit and reset, models, addresses (an address as its own network,
// a network masked), an expiry; a limit with no reset never resets.
func TestCreateKeyWithSettings(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	tomorrow := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	set := f.createKey(s, `{"name":"Set","expires_at":"`+tomorrow.Format(time.RFC3339)+`","spend_limit":{"amount":"5","currency":"USD"},`+
		`"limit_reset":"weekly","models":["gpt-5","claude-opus"],"ip_allow":["203.0.113.7","10.1.2.3/8"]}`).Key
	checkKey(t, "a key with settings", set, keyView{
		Name: "Set", Prefix: "sk-ax-v1-", Limit: "5", Remaining: "5", Reset: "weekly", Models: []string{"gpt-5", "claude-opus"},
		IPs: []string{"203.0.113.7/32", "10.0.0.0/8"}, Expires: tomorrow,
	})
	forever := f.createKey(s, `{"name":"No reset","spend_limit":{"amount":"0","currency":"USD"}}`).Key
	checkKey(t, "a limit that never resets", forever, keyView{Name: "No reset", Prefix: "sk-ax-v1-", Limit: "0", Remaining: "0"})
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action = 'api_key.create'"); got != "2" {
		t.Errorf("%s audit entries for two keys", got)
	}
}

// TestKeyReads: the list is newest first and pages by cursor, and one key is read by its ID.
func TestKeyReads(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	var ids []string
	for _, name := range []string{"one", "two", "three"} {
		ids = append(ids, f.createKey(s, `{"name":"`+name+`"}`).Key.Id)
	}
	var first, second consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys?limit=2", "", as(s)), &first)
	if diff := cmp.Diff([]string{ids[2], ids[1]}, idsOf(first)); diff != "" || first.NextCursor == nil {
		t.Fatalf("the first page (-want +got):\n%s\ncursor %v", diff, first.NextCursor)
	}
	decode(t, f.do(http.MethodGet, "/keys?limit=2&cursor="+*first.NextCursor, "", as(s)), &second)
	if diff := cmp.Diff([]string{ids[0]}, idsOf(second)); diff != "" || second.NextCursor != nil {
		t.Errorf("the second page (-want +got):\n%s\ncursor %v", diff, second.NextCursor)
	}
	expect(t, "a cursor that is none", f.do(http.MethodGet, "/keys?cursor=nonsense", "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	var one consoleapi.Key
	decode(t, f.do(http.MethodGet, "/keys/"+ids[1], "", as(s)), &one)
	if one.Id != ids[1] || one.Name != "two" {
		t.Errorf("getKey: %+v", one)
	}
	f.assertNotFound(s, "/keys/key_00000000000000000000000000")
	f.assertNotFound(s, "/keys/not-an-id")
}

func idsOf(list consoleapi.KeyList) []string {
	var ids []string
	for _, k := range list.Data {
		ids = append(ids, k.Id)
	}
	return ids
}

// assertNotFound: a GET of path is a 404 not_found whether the ID is unknown or malformed.
func (f consoleFixture) assertNotFound(s session, path string) {
	f.t.Helper()
	expect(f.t, "GET "+path, f.do(http.MethodGet, path, "", as(s)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
}

// TestUpdateKey: the settings are replaced whole, so what the request leaves out is cleared; a key can be disabled and
// enabled again; the expiry stays.
func TestUpdateKey(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	tomorrow := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	key := f.createKey(s, `{"name":"Before","expires_at":"`+tomorrow.Format(time.RFC3339)+`","models":["a"],"ip_allow":["10.0.0.0/8"]}`).Key
	var after consoleapi.Key
	rec := f.do(http.MethodPut, "/keys/"+key.Id, `{"name":"After","disabled":true}`, as(s))
	expect(t, "updateKey", rec, http.StatusOK, "")
	decode(t, rec, &after)
	checkKey(t, "after the update", after, keyView{Name: "After", Prefix: "sk-ax-v1-", Disabled: true, Expires: tomorrow})
	rec = f.do(http.MethodPut, "/keys/"+key.Id, `{"name":"After","disabled":false,"spend_limit":{"amount":"7","currency":"USD"},"limit_reset":"monthly"}`, as(s))
	expect(t, "enabling again", rec, http.StatusOK, "")
	decode(t, rec, &after)
	checkKey(t, "enabled again", after, keyView{Name: "After", Prefix: "sk-ax-v1-", Limit: "7", Remaining: "7", Reset: "monthly", Expires: tomorrow})
	expect(t, "an update of a key that is none", f.do(http.MethodPut, "/keys/key_00000000000000000000000000", `{"name":"x","disabled":false}`, as(s)),
		http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action = 'api_key.update'"); got != "2" {
		t.Errorf("%s audit entries for two updates", got)
	}
}

// TestDeleteKey: a deleted key is gone from the list and the read, and cannot be deleted or changed again.
func TestDeleteKey(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	key := f.createKey(s, `{"name":"Doomed"}`).Key
	expect(t, "deleteKey", f.do(http.MethodDelete, "/keys/"+key.Id, "", as(s)), http.StatusNoContent, "")
	f.assertNotFound(s, "/keys/"+key.Id)
	expect(t, "deleting it again", f.do(http.MethodDelete, "/keys/"+key.Id, "", as(s)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	expect(t, "changing it", f.do(http.MethodPut, "/keys/"+key.Id, `{"name":"x","disabled":false}`, as(s)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	var list consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys", "", as(s)), &list)
	if len(list.Data) != 0 {
		t.Errorf("the list after deleting: %+v", list)
	}
}

// TestKeysOfAnotherAccount: a user sees and changes only their own account's keys; another account's are as good as
// absent.
func TestKeysOfAnotherAccount(t *testing.T) {
	f := newConsoleFixture(t)
	owner := f.owner()
	theirs := f.createKey(owner, `{"name":"Theirs"}`).Key
	var info consoleapi.Session
	decode(t, f.do(http.MethodGet, "/session", "", as(owner)), &info)
	registered := f.do(http.MethodPost, "/auth/register", `{"invite_code":"`+info.User.InviteCode+`","email":"other@example.com","password":"another horse battery"}`)
	expect(t, "register", registered, http.StatusCreated, "")
	other := f.signIn("other@example.com", "another horse battery")
	var list consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys", "", as(other)), &list)
	if len(list.Data) != 0 {
		t.Errorf("another account's list: %+v", list)
	}
	f.assertNotFound(other, "/keys/"+theirs.Id)
	for _, r := range []*httptest.ResponseRecorder{
		f.do(http.MethodPut, "/keys/"+theirs.Id, `{"name":"mine now","disabled":true}`, as(other)),
		f.do(http.MethodDelete, "/keys/"+theirs.Id, "", as(other)),
	} {
		expect(t, "changing another account's key", r, http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	}
	var kept consoleapi.Key
	decode(t, f.do(http.MethodGet, "/keys/"+theirs.Id, "", as(owner)), &kept)
	if kept.Name != "Theirs" || kept.Disabled {
		t.Errorf("the owner's key after the attempts: %+v", kept)
	}
}
