package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pgtest"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestKeyFilters: the URL's name, prefix, status and created range reach the query, the status tells an expired key
// from a disabled one, and a filtered page follows its cursor. An inverted range is a 400 naming the field.
func TestKeyFilters(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	alpha := f.createKey(s, `{"name":"Alpha One"}`).Key
	f.createKey(s, `{"name":"Beta"}`)
	f.createKey(s, `{"name":"Alpha Two"}`)
	expect(t, "disabling a key", f.do(http.MethodPut, "/keys/"+alpha.Id, `{"name":"Alpha One","disabled":true}`, as(s)), http.StatusOK, "")
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		`UPDATE accounts.api_keys SET prefix = 'old-', expires_at = now() - interval '1 minute' WHERE name = 'Beta'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rangeQuery := func(from, to time.Time) string {
		return "created_after=" + url.QueryEscape(from.Format(time.RFC3339)) +
			"&created_before=" + url.QueryEscape(to.Format(time.RFC3339))
	}
	for _, c := range []struct {
		name, query string
		want        []string
	}{
		{"a name, ignoring case", "name=ALPHA", []string{"Alpha Two", "Alpha One"}},
		{"an old prefix", "prefix=OLD-", []string{"Beta"}},
		{"active keys", "status=active", []string{"Alpha Two"}},
		{"disabled keys", "status=disabled", []string{"Alpha One"}},
		{"expired keys, enabled or not", "status=expired", []string{"Beta"}},
		{"a name and a status", "name=alpha&status=disabled", []string{"Alpha One"}},
		{"an empty filter", "name=", []string{"Alpha Two", "Beta", "Alpha One"}},
		{"a range around every key", rangeQuery(now.Add(-time.Hour), now.Add(time.Hour)), []string{"Alpha Two", "Beta", "Alpha One"}},
		{"a range before every key", "created_before=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339)), []string{}},
		{"a range after every key", "created_after=" + url.QueryEscape(now.Add(time.Hour).Format(time.RFC3339)), []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var list consoleapi.KeyList
			decode(t, f.do(http.MethodGet, "/keys?"+c.query, "", as(s)), &list)
			if diff := cmp.Diff(c.want, namesOf(list)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
	var page, next consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys?name=alpha&limit=1", "", as(s)), &page)
	if diff := cmp.Diff([]string{"Alpha Two"}, namesOf(page)); diff != "" || page.NextCursor == nil {
		t.Fatalf("the first filtered page (-want +got):\n%s\ncursor %v", diff, page.NextCursor)
	}
	decode(t, f.do(http.MethodGet, "/keys?name=alpha&limit=1&cursor="+*page.NextCursor, "", as(s)), &next)
	if diff := cmp.Diff([]string{"Alpha One"}, namesOf(next)); diff != "" || next.NextCursor != nil {
		t.Errorf("the next filtered page (-want +got):\n%s\ncursor %v", diff, next.NextCursor)
	}
	for _, c := range []struct{ name, query string }{
		{"an inverted created range", rangeQuery(now, now.Add(-time.Hour))},
		{"an unknown status", "status=unknown"},
		{"a page size below one", "limit=0"},
		{"a page size above the contract", "limit=201"},
	} {
		expect(t, c.name, f.do(http.MethodGet, "/keys?"+c.query, "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
}

// TestKeyFilterEcho: a filtered page says which filters its node applied, and an unfiltered one still carries the
// object with no filter set, so a page can tell this node from one that ignores the parameters.
func TestKeyFilterEcho(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	f.createKey(s, `{"name":"Alpha One"}`)
	from := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	to := from.Add(2 * time.Hour)
	var page consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys?name=alpha&prefix=SK-AX&status=active"+
		"&created_after="+url.QueryEscape(from.Format(time.RFC3339))+
		"&created_before="+url.QueryEscape(to.Format(time.RFC3339)), "", as(s)), &page)
	name, prefix, status := "alpha", "SK-AX", consoleapi.KeyStatusActive
	want := &consoleapi.KeyFilterEcho{Name: &name, Prefix: &prefix, Status: &status, CreatedAfter: &from, CreatedBefore: &to}
	if diff := cmp.Diff(want, page.Filters); diff != "" {
		t.Errorf("the filters a page was read with (-want +got):\n%s", diff)
	}
	var all consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys", "", as(s)), &all)
	if diff := cmp.Diff(&consoleapi.KeyFilterEcho{}, all.Filters); diff != "" {
		t.Errorf("an unfiltered page's echo (-want +got):\n%s", diff)
	}
}

// TestKeyRetiredModelHint: a key whose model restrictions name a retired model says which ones, while a key with no
// retired name omits the hint.
func TestKeyRetiredModelHint(t *testing.T) {
	t.Parallel()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	if err := pgtest.Run(t.Context(), c.database.Superuser,
		`INSERT INTO catalog.author (slug, name) VALUES ('acme', 'Acme')`,
		`WITH m AS (
			INSERT INTO catalog.model (author, slug, status, version_basis, price_source)
			VALUES ('acme', 'acme/retired', 'retired', 'dated', 'manual'), ('acme', 'acme/listed', 'listed', 'dated', 'manual')
			RETURNING id, slug)
		INSERT INTO catalog.name (name, norm, visibility, model_id) SELECT slug, slug, 'public', id FROM m`); err != nil {
		t.Fatal(err)
	}
	f := startNode(c, &testClock{now: time.Now()})
	s := f.owner()
	old := f.createKey(s, `{"name":"Old","models":["acme/retired","acme/listed","acme/unknown"]}`).Key
	retired := []string{"acme/retired"}
	if diff := cmp.Diff(&retired, old.RetiredModels); diff != "" {
		t.Errorf("the retired names of a key that names one (-want +got):\n%s", diff)
	}
	var read consoleapi.Key
	decode(t, f.do(http.MethodGet, "/keys/"+old.Id, "", as(s)), &read)
	if diff := cmp.Diff(&retired, read.RetiredModels); diff != "" {
		t.Errorf("the retired names when the key is read (-want +got):\n%s", diff)
	}
	fresh := f.createKey(s, `{"name":"Fresh","models":["acme/listed"]}`).Key
	if fresh.RetiredModels != nil {
		t.Errorf("a key that names no retired model: %v", *fresh.RetiredModels)
	}
	var list consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys", "", as(s)), &list)
	hints := make(map[string]*[]string, len(list.Data))
	for _, k := range list.Data {
		hints[k.Name] = k.RetiredModels
	}
	if diff := cmp.Diff(map[string]*[]string{"Old": &retired, "Fresh": nil}, hints); diff != "" {
		t.Errorf("the retired names in the list (-want +got):\n%s", diff)
	}
}

func namesOf(list consoleapi.KeyList) []string {
	names := []string{}
	for _, k := range list.Data {
		names = append(names, k.Name)
	}
	return names
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
