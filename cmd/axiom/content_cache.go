package main

import (
	"archive/tar"
	"bufio"
	"context"
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
	stats  gateway.CacheStats
	reader cacheOpener
}

type cacheMetaSource interface {
	Stream(context.Context, gateway.CacheQuery, func(gateway.CacheMeta) error) (gateway.CacheStats, error)
}

type dbCacheSource struct{ db *pg.DB }

func (s dbCacheSource) Stream(ctx context.Context, q gateway.CacheQuery, each func(gateway.CacheMeta) error) (gateway.CacheStats, error) {
	var stats gateway.CacheStats
	err := s.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		stats, err = gateway.StreamCacheMeta(ctx, tx, q, each)
		return err
	})
	return stats, err
}

type cacheOpener interface {
	Open(context.Context, gateway.CacheMeta) ([]byte, error)
}

type dbCacheOpener struct {
	db   *pg.DB
	keys *secret.Keyring
}

func (o dbCacheOpener) Open(ctx context.Context, m gateway.CacheMeta) ([]byte, error) {
	var payload []byte
	err := o.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		payload, err = gateway.ReadCachePayload(ctx, tx, o.keys, m)
		return err
	})
	return payload, err
}

// prepareCacheExport writes the cache metadata to a private temporary file inside one snapshot. Bodies are read after
// the snapshot closes, like the content manifest.
func prepareCacheExport(ctx context.Context, source cacheMetaSource, q gateway.CacheQuery) (*os.File, gateway.CacheStats, error) {
	file, err := os.CreateTemp("", "axiom-cache-export-*.jsonl")
	if err != nil {
		return nil, gateway.CacheStats{}, err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		discard(file)
		removeTemp(path)
		return nil, gateway.CacheStats{}, err
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	stats, err := source.Stream(ctx, q, func(m gateway.CacheMeta) error {
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
		if _, err := writer.Write(line); err != nil {
			return err
		}
		return writer.WriteByte('\n')
	})
	if err == nil {
		err = writer.Flush()
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		discard(file)
		removeTemp(path)
		return nil, gateway.CacheStats{}, err
	}
	return file, stats, nil
}

func writeCacheManifestEntries(tw *tar.Writer, meta *os.File, now time.Time) error {
	return writeMetadataFile(tw, meta, "cache.jsonl", "cache metadata", now)
}

func writeCacheBodies(ctx context.Context, tw *tar.Writer, meta *os.File, reader cacheOpener) error {
	scanner := bufio.NewScanner(meta)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if err := writeCacheBody(ctx, tw, scanner.Bytes(), reader); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("content export: read cache manifest: %w", err)
	}
	if _, err := meta.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("content export: rewind cache metadata: %w", err)
	}
	return nil
}

func writeCacheBody(ctx context.Context, tw *tar.Writer, line []byte, reader cacheOpener) error {
	var entry cacheManifestEntry
	if err := jsonx.UnmarshalKnown(line, &entry); err != nil {
		return fmt.Errorf("content export: cache metadata entry: %w", err)
	}
	m, err := entry.meta()
	if err != nil {
		return err
	}
	payload, err := reader.Open(ctx, m)
	if err != nil {
		return err
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
