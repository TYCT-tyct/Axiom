package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"os"
	"testing"
	"time"
	"uuid"

	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

//nolint:cyclop,gocognit // one export through cache, encryption and tar verification
func TestContentExportIncludesResponseCache(t *testing.T) {
	c := newCommandLine(t)
	master := masterKeys(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	site, account := uuid.NewV7(), uuid.NewV7()
	cacheID, sourceID, endpoint := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	body := []byte("cached reply")
	payload := append(append([]byte("turn-state"), 0), body...)
	sealed := master.Seal(secret.New(string(payload)), secret.Location{
		Table: "gateway.response_cache", RowID: cacheID.String(), Purpose: "body",
	})
	if err := c.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO gateway.response_cache
			(id, site_id, account_id, cache_key, source_id, format, model, endpoint, ciphertext, nonce,
			 wrapped_dek, key_version, body_size, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, 0, 'model', $6, $7, $8, $9, $10, $11, $12, $13)`,
			cacheID, site, account, bytes.Repeat([]byte{1}, 32), sourceID, endpoint,
			sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion, len(body), now.Add(-30*time.Minute),
			now.Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	contentMeta, contentStats, err := prepareContentExport(t.Context(), fixedContentSource{},
		observability.ContentQuery{Since: now.Add(-2 * time.Hour), Until: now})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupTemp(contentMeta)
	cacheMeta, cacheStats, err := prepareCacheExport(t.Context(), dbCacheSource{c.db}, gateway.CacheQuery{
		Since: now.Add(-2 * time.Hour), Until: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupTemp(cacheMeta)
	if cacheStats.Count != 1 || cacheStats.Bytes != int64(len(payload)) {
		t.Fatalf("cache stats %+v, want one payload of %d bytes", cacheStats, len(payload))
	}
	cache := &cacheExport{meta: cacheMeta, stats: cacheStats, reader: dbCacheOpener{db: c.db, keys: master}}

	recipient, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval, raw, digest, _, _ := contentTestApproval(t, now, recipient.PublicKey().Bytes())
	exportID := uuid.NewV7()
	if err := beginContentExport(t.Context(), c.db, exportID, approval, digest, contentStats, cacheStats); err != nil {
		t.Fatal(err)
	}
	var storedCount, storedBytes int64
	if err := c.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT cache_count, cache_bytes FROM observability.content_exports WHERE id = $1`, exportID).
			Scan(&storedCount, &storedBytes)
	}); err != nil {
		t.Fatal(err)
	}
	if storedCount != cacheStats.Count || storedBytes != cacheStats.Bytes {
		t.Fatalf("stored cache stats %d/%d, want %+v", storedCount, storedBytes, cacheStats)
	}
	var encrypted bytes.Buffer
	if err := writeContentExportWithCache(t.Context(), &encrypted, approval, raw, digest, recipient.PublicKey(),
		contentMeta, contentStats, cache, fixedContentOpener{}, now); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Bytes(), payload) {
		t.Fatal("the encrypted export contains the cache payload")
	}
	var tarGz bytes.Buffer
	if _, err := decryptContentExport(&tarGz, bytes.NewReader(encrypted.Bytes()), recipient); err != nil {
		t.Fatal(err)
	}
	files := readExportTar(t, tarGz.Bytes())
	var manifest struct {
		CacheCount int   `json:"cache_count"`
		CacheBytes int64 `json:"cache_bytes"`
	}
	if err := jsonx.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.CacheCount != 1 || manifest.CacheBytes != int64(len(payload)) {
		t.Fatalf("manifest says %+v", manifest)
	}
	if !bytes.Equal(files["cache/"+cacheID.String()], payload) {
		t.Fatal("the cache payload differs")
	}
	lines := bytes.Split(bytes.TrimSpace(files["cache.jsonl"]), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("cache.jsonl has %d lines", len(lines))
	}
	var entry cacheManifestEntry
	if err := jsonx.UnmarshalKnown(lines[0], &entry); err != nil {
		t.Fatal(err)
	}
	if entry.CacheID != cacheID.String() || entry.Path != "cache/"+cacheID.String() || entry.Size != int64(len(payload)) {
		t.Fatalf("cache manifest entry is %+v", entry)
	}
}

func cleanupTemp(file *os.File) {
	discard(file)
	removeTemp(file.Name())
}
