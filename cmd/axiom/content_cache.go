package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"uuid"

	"axiom/internal/gateway"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

const (
	maxCacheStageField = 2 << 20
	maxCacheStageBody  = 1 << 20
)

// cacheManifestEntry is one line of cache.jsonl. The cache is a separate, short-lived source: it has no request ID, so
// its rows are identified by cache ID and kept out of the content reference list.
type cacheManifestEntry struct {
	CacheID   string `json:"cache_id"`
	SiteID    string `json:"site_id"`
	AccountID string `json:"account_id"`
	SourceID  string `json:"source_id"`
	Format    uint8  `json:"format"`
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`
}

type cacheExport struct {
	meta   *os.File
	bodies *os.File
	stats  gateway.CacheStats
	stage  cipher.AEAD
}

func (c *cacheExport) cleanup() {
	if c == nil {
		return
	}
	if c.meta != nil {
		discard(c.meta)
		removeTemp(c.meta.Name())
	}
	if c.bodies != nil {
		discard(c.bodies)
		removeTemp(c.bodies.Name())
	}
}

type cacheMetaSource interface {
	Stream(context.Context, gateway.CacheQuery, func(gateway.CacheMeta, secret.Sealed, int) error) (gateway.CacheStats, error)
}

type dbCacheSource struct{ db *pg.DB }

func (s dbCacheSource) Stream(ctx context.Context, q gateway.CacheQuery,
	each func(gateway.CacheMeta, secret.Sealed, int) error,
) (gateway.CacheStats, error) {
	var stats gateway.CacheStats
	err := s.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		stats, err = gateway.StreamCacheSealed(ctx, tx, q, each)
		return err
	})
	return stats, err
}

// prepareCacheExport writes one snapshot's cache metadata and re-encrypted payloads to private temporary files. It
// opens each database-sealed payload while the row's snapshot is still open, then encrypts it with a per-export
// ephemeral key. A later cache eviction, replacement or keyring retirement cannot break the export, and no plaintext
// cache body is written to disk. The transaction holds only for this read, decryption and local staging write, not for
// the object-store content reads or the final archive.
func prepareCacheExport(ctx context.Context, source cacheMetaSource, q gateway.CacheQuery,
	keys *secret.Keyring,
) (*cacheExport, error) {
	if source == nil || keys == nil {
		return nil, errors.New("content export: cache snapshot needs a source and the platform keyring")
	}
	cache, err := newCacheExport()
	if err != nil {
		return nil, err
	}
	metaWriter := bufio.NewWriterSize(cache.meta, 64<<10)
	bodyWriter := bufio.NewWriterSize(cache.bodies, 64<<10)
	stats, err := source.Stream(ctx, q, func(m gateway.CacheMeta, sealed secret.Sealed, bodySize int) error {
		payload, err := gateway.OpenCachePayload(m, sealed, bodySize, keys)
		if err != nil {
			return err
		}
		return stageCache(metaWriter, bodyWriter, cache.stage, m, bodySize, payload)
	})
	if err == nil {
		err = cache.finishStaging(metaWriter, bodyWriter)
	}
	if err != nil {
		cache.cleanup()
		return nil, err
	}
	cache.stats = stats
	return cache, nil
}

func newCacheExport() (*cacheExport, error) {
	meta, err := os.CreateTemp("", "axiom-cache-export-*.jsonl")
	if err != nil {
		return nil, err
	}
	bodies, err := os.CreateTemp("", "axiom-cache-export-*.bin")
	if err != nil {
		discard(meta)
		removeTemp(meta.Name())
		return nil, err
	}
	stage, err := newCacheStage()
	if err != nil {
		discard(meta)
		removeTemp(meta.Name())
		discard(bodies)
		removeTemp(bodies.Name())
		return nil, err
	}
	cache := &cacheExport{meta: meta, bodies: bodies, stage: stage}
	for _, file := range []*os.File{meta, bodies} {
		if err := file.Chmod(0o600); err != nil {
			cache.cleanup()
			return nil, err
		}
	}
	return cache, nil
}

func newCacheStage() (cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("content export: cache staging key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("content export: cache staging cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("content export: cache staging AEAD: %w", err)
	}
	return aead, nil
}

func stageCache(metaWriter, bodyWriter *bufio.Writer, stage cipher.AEAD, m gateway.CacheMeta,
	bodySize int, payload []byte,
) error {
	line, err := jsonx.MarshalCanonical(cacheManifestEntry{
		CacheID:   m.ID.String(),
		SiteID:    m.Site.String(),
		AccountID: m.Account.String(),
		SourceID:  m.Source.String(),
		Format:    m.Format,
		Model:     m.Model,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt: m.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Size:      m.Size,
		Path:      m.Path,
	})
	if err != nil {
		return err
	}
	if _, err := metaWriter.Write(line); err != nil {
		return err
	}
	if err := metaWriter.WriteByte('\n'); err != nil {
		return err
	}
	return writeCacheStaged(bodyWriter, stage, m.ID, bodySize, payload)
}

func (c *cacheExport) finishStaging(metaWriter, bodyWriter *bufio.Writer) error {
	if err := metaWriter.Flush(); err != nil {
		return err
	}
	if err := bodyWriter.Flush(); err != nil {
		return err
	}
	if _, err := c.meta.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := c.bodies.Seek(0, io.SeekStart)
	return err
}

func writeCacheManifestEntries(tw *tar.Writer, meta *os.File, now time.Time) error {
	return writeMetadataFile(tw, meta, "cache.jsonl", "cache metadata", now)
}

func writeCacheBodies(tw *tar.Writer, cache *cacheExport) error {
	meta := bufio.NewScanner(cache.meta)
	meta.Buffer(make([]byte, 64<<10), 1<<20)
	bodies := bufio.NewReaderSize(cache.bodies, 64<<10)
	for meta.Scan() {
		if err := writeCacheBody(tw, meta.Bytes(), bodies, cache); err != nil {
			return err
		}
	}
	if err := meta.Err(); err != nil {
		return fmt.Errorf("content export: read cache manifest: %w", err)
	}
	if _, err := bodies.ReadByte(); err != io.EOF {
		if err == nil {
			return errors.New("content export: cache staging has trailing payloads")
		}
		return fmt.Errorf("content export: read cache staging: %w", err)
	}
	return nil
}

func writeCacheBody(tw *tar.Writer, line []byte, bodies io.Reader, cache *cacheExport) error {
	var entry cacheManifestEntry
	if err := jsonx.UnmarshalKnown(line, &entry); err != nil {
		return fmt.Errorf("content export: cache metadata entry: %w", err)
	}
	m, err := entry.meta()
	if err != nil {
		return err
	}
	payload, bodySize, err := readCacheStaged(bodies, cache.stage, m.ID)
	if err != nil {
		return err
	}
	nul := bytes.IndexByte(payload, 0)
	if nul < 0 || len(payload)-nul-1 != bodySize {
		return fmt.Errorf("content export: %s has an invalid cache payload", m.Path)
	}
	if int64(len(payload)) != m.Size {
		return fmt.Errorf("content export: %s is %d bytes, the manifest says %d", m.Path, len(payload), m.Size)
	}
	if err := tw.WriteHeader(&tar.Header{Name: m.Path, Mode: 0o600, Size: m.Size, ModTime: m.CreatedAt.UTC()}); err != nil {
		return fmt.Errorf("content export: header for %s: %w", m.Path, err)
	}
	if _, err := tw.Write(payload); err != nil {
		return fmt.Errorf("content export: write %s: %w", m.Path, err)
	}
	return nil
}

func writeCacheStaged(w io.Writer, stage cipher.AEAD, id uuid.UUID, bodySize int, payload []byte) error {
	if bodySize < 0 || bodySize > maxCacheStageBody {
		return fmt.Errorf("content export: cache body size %d", bodySize)
	}
	if len(payload) > maxCacheStageField {
		return fmt.Errorf("content export: cache payload is too large: %d", len(payload))
	}
	aad := cacheStageAAD(id, bodySize)
	staged := stage.Seal(nil, nil, payload, aad) // #nosec G407 -- NewGCMWithRandomNonce generates the nonce.
	if len(staged) > maxCacheStageField {
		return fmt.Errorf("content export: cache staging payload is too large: %d", len(staged))
	}
	if err := binary.Write(w, binary.BigEndian, uint32(bodySize)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint64(len(staged))); err != nil {
		return err
	}
	_, err := w.Write(staged)
	return err
}

func readCacheStaged(r io.Reader, stage cipher.AEAD, id uuid.UUID) ([]byte, int, error) {
	var bodySize uint32
	if err := binary.Read(r, binary.BigEndian, &bodySize); err != nil {
		return nil, 0, fmt.Errorf("content export: read cache staging body size: %w", err)
	}
	if bodySize > maxCacheStageBody {
		return nil, 0, fmt.Errorf("content export: cache staging body is too large: %d", bodySize)
	}
	var size uint64
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return nil, 0, fmt.Errorf("content export: read cache staging length: %w", err)
	}
	if size > maxCacheStageField {
		return nil, 0, fmt.Errorf("content export: cache staging field is too large: %d", size)
	}
	staged := make([]byte, size)
	if _, err := io.ReadFull(r, staged); err != nil {
		return nil, 0, fmt.Errorf("content export: read cache staging field: %w", err)
	}
	if len(staged) < stage.NonceSize()+stage.Overhead() {
		return nil, 0, errors.New("content export: cache staging payload is truncated")
	}
	payload, err := stage.Open(nil, staged[:stage.NonceSize()], staged[stage.NonceSize():],
		cacheStageAAD(id, int(bodySize)))
	if err != nil {
		return nil, 0, fmt.Errorf("content export: open cache staging payload: %w", err)
	}
	return payload, int(bodySize), nil
}

func cacheStageAAD(id uuid.UUID, bodySize int) []byte {
	aad := []byte("axiom/content-export/cache-stage/v1\x00")
	aad = append(aad, id[:]...)
	return binary.BigEndian.AppendUint32(aad, uint32(bodySize)) //nolint:gosec // bounded by maxCacheStageBody
}

func (e cacheManifestEntry) meta() (gateway.CacheMeta, error) {
	var m gateway.CacheMeta
	var err error
	if m.ID, err = uuid.Parse(e.CacheID); err != nil {
		return m, fmt.Errorf("content export: cache id: %w", err)
	}
	if m.Site, err = uuid.Parse(e.SiteID); err != nil {
		return m, fmt.Errorf("content export: cache site id: %w", err)
	}
	if m.Account, err = uuid.Parse(e.AccountID); err != nil {
		return m, fmt.Errorf("content export: cache account id: %w", err)
	}
	if m.Source, err = uuid.Parse(e.SourceID); err != nil {
		return m, fmt.Errorf("content export: cache source id: %w", err)
	}
	if m.CreatedAt, err = time.Parse(time.RFC3339Nano, e.CreatedAt); err != nil {
		return m, fmt.Errorf("content export: cache created_at: %w", err)
	}
	if m.ExpiresAt, err = time.Parse(time.RFC3339Nano, e.ExpiresAt); err != nil {
		return m, fmt.Errorf("content export: cache expires_at: %w", err)
	}
	if e.Size <= 0 {
		return m, fmt.Errorf("content export: cache size %d", e.Size)
	}
	if e.Path != "cache/"+m.ID.String() {
		return m, fmt.Errorf("content export: cache path %q", e.Path)
	}
	m.Format, m.Model, m.Size, m.Path = e.Format, e.Model, e.Size, e.Path
	return m, nil
}
