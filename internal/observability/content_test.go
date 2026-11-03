package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// memoryObjects is the object store a test reads sealed bodies from.
type memoryObjects struct {
	objects map[string][]byte
}

func (m *memoryObjects) Get(_ context.Context, key string) (*objectstore.Object, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return &objectstore.Object{
		ObjectInfo: objectstore.ObjectInfo{Key: key, Size: int64(len(data))}, //nolint:modernize // keep the embedded field type at the call site
		Body:       io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func contentKeyFor(t *testing.T, db *pg.DB, keys *secret.Keyring, site, account uuid.UUID) [32]byte {
	t.Helper()
	key := sha256.Sum256([]byte("content test " + account.String()))
	sealed := keys.Seal(secret.New(string(key[:])), secret.Location{
		Table: "observability.content_keys", RowID: account.String(), Purpose: "content_key",
	})
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO observability.content_keys
			(account_id, site_id, ciphertext, nonce, wrapped_key, key_version, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (account_id) DO NOTHING`,
			account, site, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatalf("insert a content key: %v", err)
	}
	return key
}

// testContentKeys is the command's key source: one platform-scope read per account, exactly as cmd wires it.
type testContentKeys struct {
	db   *pg.DB
	keys *secret.Keyring
}

func (s testContentKeys) ContentKey(ctx context.Context, account uuid.UUID) ([32]byte, error) {
	var key [32]byte
	err := s.db.ReadTx(ctx, platformScope, func(tx pg.Tx) error {
		var err error
		key, err = ReadContentKey(ctx, tx, s.keys, account)
		return err
	})
	return key, err
}

func putContent(t *testing.T, db *pg.DB, store *memoryObjects, key [32]byte, site, account uuid.UUID,
	at time.Time, kind ContentKind, plaintext []byte,
) ContentMeta {
	t.Helper()
	return putContentPart(t, db, store, key, site, account, uuid.UUID{}, at, kind, "body", plaintext)
}

func putContentPart(t *testing.T, db *pg.DB, store *memoryObjects, key [32]byte, site, account,
	requestID uuid.UUID, at time.Time, kind ContentKind, part string, plaintext []byte,
) ContentMeta {
	t.Helper()
	if requestID == (uuid.UUID{}) {
		requestID = uuid.NewV7()
	}
	m := ContentMeta{
		RequestID: requestID, Part: part, Kind: kind, Site: site, Account: account, Blob: uuid.NewV7(),
		CreatedAt: at.UTC(), Size: int64(len(plaintext)),
	}
	_, mac := contentKeys(key)
	m.Digest = contentDigest(mac, plaintext)
	m.ObjectKey = "content/v1/" + site.String() + "/" + account.String() + "/" + m.Blob.String()
	object, err := sealContent(key, plaintext, m)
	if err != nil {
		t.Fatalf("seal content: %v", err)
	}
	store.objects[m.ObjectKey] = object
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO observability.content_blobs
			(id, site_id, account_id, digest, object_key, size, created_at, last_referenced_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
			m.Blob, m.Site, m.Account, m.Digest[:], m.ObjectKey, m.Size, m.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO observability.content_refs
			(request_id, part, kind, site_id, account_id, blob_id, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			m.RequestID, m.Part, string(m.Kind), m.Site, m.Account, m.Blob, m.CreatedAt)
		return err
	}); err != nil {
		t.Fatalf("insert content: %v", err)
	}
	return m
}

func TestContentSealOpen(t *testing.T) {
	key := sha256.Sum256([]byte("account key"))
	plaintext := []byte(`{"model":"claude","messages":["hello"]}`)
	m := ContentMeta{
		RequestID: uuid.NewV7(), Kind: ContentRequest, Site: uuid.NewV7(), Account: uuid.NewV7(),
		Blob: uuid.NewV7(), Size: int64(len(plaintext)),
	}
	_, mac := contentKeys(key)
	m.Digest = contentDigest(mac, plaintext)
	object, err := sealContent(key, plaintext, m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openContent(key, object, m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("opened %q, want %q", got, plaintext)
	}

	t.Run("wrong digest in the reference", func(t *testing.T) {
		changed := m
		changed.Digest[0] ^= 1
		if _, err := openContent(key, object, changed); err == nil {
			t.Fatal("a wrong digest opened")
		}
	})
	t.Run("wrong account", func(t *testing.T) {
		changed := m
		changed.Account = uuid.NewV7()
		if _, err := openContent(key, object, changed); err == nil {
			t.Fatal("another account's body opened")
		}
	})
	t.Run("tampered ciphertext", func(t *testing.T) {
		changed := append([]byte(nil), object...)
		changed[len(changed)-1] ^= 1
		if _, err := openContent(key, changed, m); err == nil {
			t.Fatal("a tampered body opened")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, err := openContent(key, object[:len(object)-1], m); err == nil {
			t.Fatal("a truncated body opened")
		}
	})
	t.Run("seal refuses a different digest", func(t *testing.T) {
		changed := m
		changed.Digest[0] ^= 1
		if _, err := sealContent(key, plaintext, changed); err == nil {
			t.Fatal("sealed with a different digest")
		}
	})
}

//nolint:cyclop // one window through every filter and refusal.
func TestContentMetaScope(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	store := &memoryObjects{objects: map[string][]byte{}}
	siteA, siteB := uuid.NewV7(), uuid.NewV7()
	accountA, accountB := uuid.NewV7(), uuid.NewV7()
	keyA := contentKeyFor(t, db, keys, siteA, accountA)
	keyB := contentKeyFor(t, db, keys, siteB, accountB)
	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	t1 := t0.Add(time.Hour)
	putContent(t, db, store, keyA, siteA, accountA, t0, ContentRequest, []byte("a-request"))
	putContent(t, db, store, keyA, siteA, accountA, t1, ContentResponse, []byte("a-response"))
	putContent(t, db, store, keyB, siteB, accountB, t0, ContentRequest, []byte("b-request"))
	putContent(t, db, store, keyA, siteA, accountA, t1, ContentAttempt, []byte("a-attempt"))

	collect := func(q ContentQuery) ([]ContentMeta, ContentStats, error) {
		var got []ContentMeta
		var stats ContentStats
		err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			var err error
			stats, err = StreamContentMeta(t.Context(), tx, q, func(m ContentMeta) error {
				got = append(got, m)
				return nil
			})
			return err
		})
		return got, stats, err
	}
	window := ContentQuery{Since: t0.Add(-time.Minute), Until: t1.Add(time.Minute)}
	got, stats, err := collect(window)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || stats.Count != 3 || stats.Bytes != int64(len("a-request")+len("a-response")+len("b-request")) {
		t.Fatalf("got %d rows and %+v, want three", len(got), stats)
	}
	if !got[0].CreatedAt.Equal(t0) || !got[1].CreatedAt.Equal(t0) || !got[2].CreatedAt.Equal(t1) {
		t.Fatalf("rows are not ordered by time: %+v", got)
	}
	filtered, stats, err := collect(ContentQuery{Since: t0.Add(-time.Minute), Until: t1.Add(time.Minute), Site: siteA})
	if err != nil || len(filtered) != 2 || stats.Count != 2 {
		t.Fatalf("site filter: %d rows, %+v, %v", len(filtered), stats, err)
	}
	filtered, _, err = collect(ContentQuery{Since: t0.Add(-time.Minute), Until: t1.Add(time.Minute), Account: accountB})
	if err != nil || len(filtered) != 1 || filtered[0].Account != accountB {
		t.Fatalf("account filter: %d rows, %v", len(filtered), err)
	}
	filtered, _, err = collect(ContentQuery{Since: t1, Until: t1.Add(time.Minute)})
	if err != nil || len(filtered) != 1 || filtered[0].Kind != ContentResponse {
		t.Fatalf("time filter: %d rows, %v", len(filtered), err)
	}
	if _, _, err := collect(ContentQuery{Since: t0, Until: t0.Add(MaxContentExportRange + time.Second)}); err == nil {
		t.Fatal("a range over a year was accepted")
	}

	var visible int
	if err := db.ReadTx(t.Context(), accountScope(accountA), func(tx pg.Tx) error {
		stats, err := StreamContentMeta(t.Context(), tx, window, func(ContentMeta) error { return nil })
		visible = int(stats.Count)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("an account scope saw %d content references", visible)
	}
}

func TestContentMetaMultipleParts(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	store := &memoryObjects{objects: map[string][]byte{}}
	site, account := uuid.NewV7(), uuid.NewV7()
	key := contentKeyFor(t, db, keys, site, account)
	requestID := uuid.NewV7()
	now := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	first := putContentPart(t, db, store, key, site, account, requestID, now, ContentRequest, "message-0", []byte("one"))
	second := putContentPart(t, db, store, key, site, account, requestID, now, ContentRequest, "message-1", []byte("two"))

	var got []ContentMeta
	err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := StreamContentMeta(t.Context(), tx, ContentQuery{Since: now.Add(-time.Minute), Until: now.Add(time.Minute)},
			func(m ContentMeta) error {
				got = append(got, m)
				return nil
			})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Part != "message-0" || got[1].Part != "message-1" {
		t.Fatalf("got %+v, want both parts of one request", got)
	}
	if got[0].TarPath() == got[1].TarPath() || !strings.Contains(got[0].TarPath(), "/request/message-0") ||
		!strings.Contains(got[1].TarPath(), "/request/message-1") {
		t.Fatalf("paths are not unique: %q, %q", got[0].TarPath(), got[1].TarPath())
	}
	if got[0].RequestID != requestID || got[1].RequestID != requestID || first.Blob == second.Blob {
		t.Fatalf("parts lost their request or shared a blob: %+v", got)
	}
}

func TestContentReader(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	store := &memoryObjects{objects: map[string][]byte{}}
	site, account := uuid.NewV7(), uuid.NewV7()
	key := contentKeyFor(t, db, keys, site, account)
	plaintext := []byte("the body")
	m := putContent(t, db, store, key, site, account, time.Now(), ContentRequest, plaintext)

	reader, err := NewContentReader(testContentKeys{db, keys}, store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.Open(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("opened %q, want %q", got, plaintext)
	}

	store.objects[m.ObjectKey][len(store.objects[m.ObjectKey])-1] ^= 1
	if _, err := reader.Open(t.Context(), m); err == nil {
		t.Fatal("a tampered object opened")
	}
	store.objects[m.ObjectKey] = store.objects[m.ObjectKey][:len(store.objects[m.ObjectKey])-1]
	if _, err := reader.Open(t.Context(), m); err == nil {
		t.Fatal("a truncated object opened")
	}

	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM observability.content_keys WHERE account_id = $1`, account)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewContentReader(testContentKeys{db, keys}, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Open(t.Context(), m); !errors.Is(err, ErrNoContentKey) {
		t.Fatalf("opening without a key: %v, want ErrNoContentKey", err)
	}
}

func TestContentKeyIsBoundToItsAccount(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	siteA, siteB := uuid.NewV7(), uuid.NewV7()
	accountA, accountB := uuid.NewV7(), uuid.NewV7()
	contentKeyFor(t, db, keys, siteA, accountA)
	var ciphertext, nonce, wrapped []byte
	var version uint32
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ciphertext, nonce, wrapped_key, key_version
			FROM observability.content_keys WHERE account_id = $1`, accountA).Scan(&ciphertext, &nonce, &wrapped, &version)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO observability.content_keys
			(account_id, site_id, ciphertext, nonce, wrapped_key, key_version, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, now())`, accountB, siteB, ciphertext, nonce, wrapped, version)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := ReadContentKey(t.Context(), tx, keys, accountB)
		return err
	})
	if err == nil {
		t.Fatal("a content key opened under another account's AAD")
	}
}

func TestContentTablesArePlatformOnly(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	store := &memoryObjects{objects: map[string][]byte{}}
	site, account := uuid.NewV7(), uuid.NewV7()
	key := contentKeyFor(t, db, keys, site, account)
	now := time.Now()
	putContent(t, db, store, key, site, account, now, ContentRequest, []byte("private"))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO observability.content_exports
			(id, approval_digest, owner_key, owner2_key, recipient_key, since, until, destination, format, status, started_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, '/tmp/export', 'test', 'started', $8)`,
			uuid.NewV7(), bytes.Repeat([]byte{1}, 32), strings.Repeat("a", 64), strings.Repeat("c", 64),
			strings.Repeat("b", 64), now.Add(-time.Hour), now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for name, query := range map[string]string{
		"keys":    `SELECT count(*) FROM observability.content_keys`,
		"blobs":   `SELECT count(*) FROM observability.content_blobs`,
		"refs":    `SELECT count(*) FROM observability.content_refs`,
		"exports": `SELECT count(*) FROM observability.content_exports`,
	} {
		var platform, own int
		if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), query).Scan(&platform)
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), query).Scan(&own)
		}); err != nil {
			t.Fatal(err)
		}
		if platform != 1 || own != 0 {
			t.Fatalf("%s: platform saw %d, account saw %d; want 1 and 0", name, platform, own)
		}
	}
}
