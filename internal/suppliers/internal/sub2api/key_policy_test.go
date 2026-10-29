package sub2api

import (
	"strings"
	"testing"
	"time"
)

func TestKeyExpiryFromRecordedListing(t *testing.T) {
	t.Parallel()
	table := panel(t)
	response := fixture(t, "keys")
	response.Body = []byte(strings.Replace(string(response.Body), `"expires_at": null`, `"expires_at": "2026-11-01T00:00:00Z"`, 1))
	table["GET /api/v1/keys?page=1&page_size=100"] = response
	m := manager0(t, routes(table), login(t, epoch.Add(24*time.Hour)))
	keys, err := readKeys(t, m)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if keys[0].Policy == nil || keys[0].Policy.ExpiresAt == nil || !keys[0].Policy.ExpiresAt.Equal(want) || keys[0].Policy.Models != nil {
		t.Fatalf("expiry and unrestricted models: %+v", keys[0].Policy)
	}
	if keys[1].Policy == nil || keys[1].Policy.ExpiresAt != nil {
		t.Fatalf("non-expiring key: %+v", keys[1].Policy)
	}
}
