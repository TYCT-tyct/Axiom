package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/config"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// contentManifestEntry is one line of manifest.jsonl: everything needed to find, verify and place one body. It holds
// metadata only; the body is a separate tar entry.
type contentManifestEntry struct {
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
	SiteID    string `json:"site_id"`
	AccountID string `json:"account_id"`
	BlobID    string `json:"blob_id"`
	CreatedAt string `json:"created_at"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
	ObjectKey string `json:"object_key"`
	Path      string `json:"path"`
}

// contentExportSettings is the server-side configuration: the master key file, the two public keys and the object
// store. The private keys are never here.
type contentExportSettings struct {
	masterPath, signingPath, encryptionPath string
	timeout                                 time.Duration
	objects                                 objectstore.Config
}

func readContentExportSettings(environ []string) (contentExportSettings, error) {
	env := config.New(environ)
	settings := contentExportSettings{
		masterPath:     env.Required(masterKeyFileVar),
		signingPath:    env.Required(contentSigningKeyVar),
		encryptionPath: env.Required(contentEncryptionKeyVar),
		timeout:        env.Duration(contentExportTimeoutVar, contentExportTimeout),
	}
	cfg, err := objectstore.ReadConfig(env)
	if err != nil {
		return contentExportSettings{}, fmt.Errorf("configuration:\n%w", err)
	}
	if err := env.Problems(); err != nil {
		return contentExportSettings{}, fmt.Errorf("configuration:\n%w", err)
	}
	if settings.timeout <= 0 {
		return contentExportSettings{}, errors.New("AXIOM_CONTENT_EXPORT_TIMEOUT must be positive")
	}
	if !cfg.Configured() {
		return contentExportSettings{}, errors.New("content export needs the object store settings (AXIOM_OBJECTSTORE_*)")
	}
	settings.objects = cfg
	return settings, nil
}

// contentExportCommand runs on the server. The signed approval is the only scope: there are no flags that could widen
// it, and the approval is consumed once, before the first body is read.
func contentExportCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	var approvalPath string
	if err := flags("content export", contentUsage, args, func(set *flag.FlagSet) {
		set.StringVar(&approvalPath, "approval", "", "the owner's signed approval")
	}); err != nil {
		return err
	}
	if approvalPath == "" {
		return fmt.Errorf("--approval is required\n%s", contentUsage)
	}
	settings, err := readContentExportSettings(environ)
	if err != nil {
		return err
	}
	master, err := secret.LoadMaster(settings.masterPath)
	if err != nil {
		return err
	}
	signingPub, err := readContentPublicKey(settings.signingPath)
	if err != nil {
		return err
	}
	encryptionPub, err := readContentPublicKey(settings.encryptionPath)
	if err != nil {
		return err
	}
	recipient, err := ecdh.X25519().NewPublicKey(encryptionPub)
	if err != nil {
		return fmt.Errorf("the encryption key: %w", err)
	}
	approvalData, err := readApprovalFile(approvalPath)
	if err != nil {
		return err
	}
	approval, raw, digest, err := verifyContentApproval(approvalData, signingPub, encryptionPub, time.Now())
	if err != nil {
		return err
	}
	scope, err := approval.scope()
	if err != nil {
		return err
	}
	objects, err := objectstore.Open(settings.objects)
	if err != nil {
		return err
	}
	defer objects.Close()

	return withDatabase(ctx, environ, func(db *pg.DB) error {
		ctx, cancel := context.WithTimeout(ctx, settings.timeout)
		defer cancel()
		return runContentExport(ctx, db, objects, master.Keyring(), approval, raw, digest, scope, recipient, out)
	})
}

func runContentExport(ctx context.Context, db *pg.DB, objects *objectstore.Client, keys *secret.Keyring,
	approval contentApproval, raw []byte, digest [32]byte, scope contentExportScope, recipient *ecdh.PublicKey, out io.Writer,
) error {
	if err := checkContentDestination(approval.Destination); err != nil {
		return err
	}
	reader, err := observability.NewContentReader(dbContentKeys{db, keys}, objects)
	if err != nil {
		return err
	}
	meta, stats, err := prepareContentExport(ctx, dbContentSource{db}, observability.ContentQuery{
		Since: scope.Since, Until: scope.Until, Site: scope.Site, Account: scope.Account,
	})
	if err != nil {
		return err
	}
	defer func() {
		discard(meta)
		removeTemp(meta.Name())
	}()
	exportID := uuid.NewV7()
	if err := beginContentExport(ctx, db, exportID, approval, digest, stats); err != nil {
		return err
	}
	size, sum, err := writeContentExportFile(ctx, db, exportID, approval, raw, digest, recipient, meta, stats, reader)
	if err != nil {
		return err
	}
	if err := finishContentExport(ctx, db, exportID, size, sum); err != nil {
		return fmt.Errorf("the export is at %s and its start is audited, but recording completion failed: %w", approval.Destination, err)
	}
	return writeLine(out, map[string]any{
		"done":          "content export",
		"export":        exportID.String(),
		"contents":      stats.Count,
		"content_bytes": stats.Bytes,
		"output":        approval.Destination,
		"output_bytes":  size,
		"sha256":        hex.EncodeToString(sum[:]),
	})
}

// writeContentExportFile writes one encrypted archive and publishes it only after a full fsync. Any failure after the
// start row is recorded is marked failed here, with the stage that failed.
func writeContentExportFile(ctx context.Context, db *pg.DB, exportID uuid.UUID, approval contentApproval, raw []byte,
	digest [32]byte, recipient *ecdh.PublicKey, meta *os.File, stats observability.ContentStats, reader contentOpener,
) (int64, [32]byte, error) {
	temp, err := createContentExportTemp(approval.Destination)
	if err != nil {
		failContentExport(ctx, db, exportID, "create")
		return 0, [32]byte{}, err
	}
	tempPath := temp.Name()
	hash := sha256.New()
	writeErr := writeContentExport(ctx, io.MultiWriter(temp, hash), approval, raw, digest, recipient, meta, stats, reader, time.Now())
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	if closeErr := temp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		removeTemp(tempPath)
		failContentExport(ctx, db, exportID, "write")
		return 0, [32]byte{}, writeErr
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		removeTemp(tempPath)
		failContentExport(ctx, db, exportID, "stat")
		return 0, [32]byte{}, err
	}
	if err := publishNoReplace(tempPath, approval.Destination); err != nil {
		removeTemp(tempPath)
		failContentExport(ctx, db, exportID, "publish")
		return 0, [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return info.Size(), sum, nil
}

// contentDecryptCommand decrypts on the owner's machine. It is local-only: no database or object store is opened, and
// the private encryption key never leaves the owner's control.
type contentMetaSource interface {
	Stream(context.Context, observability.ContentQuery, func(observability.ContentMeta) error) (observability.ContentStats, error)
}

type dbContentSource struct{ db *pg.DB }

func (s dbContentSource) Stream(ctx context.Context, q observability.ContentQuery, each func(observability.ContentMeta) error) (observability.ContentStats, error) {
	var stats observability.ContentStats
	err := s.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		stats, err = observability.StreamContentMeta(ctx, tx, q, each)
		return err
	})
	return stats, err
}

// dbContentKeys is the command's platform-scope key source; the observability package never builds a scope itself.
type dbContentKeys struct {
	db   *pg.DB
	keys *secret.Keyring
}

func (s dbContentKeys) ContentKey(ctx context.Context, account uuid.UUID) ([32]byte, error) {
	var key [32]byte
	err := s.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		key, err = observability.ReadContentKey(ctx, tx, s.keys, account)
		return err
	})
	return key, err
}

// contentOpener reads one body; ContentReader is the production implementation.
type contentOpener interface {
	Open(context.Context, observability.ContentMeta) ([]byte, error)
}

// prepareContentExport writes the reference list to a private temporary file inside one database snapshot. The body
// reads happen later, after the transaction is closed, so an export does not hold a connection while it waits on the
// object store.
func prepareContentExport(ctx context.Context, source contentMetaSource, q observability.ContentQuery) (*os.File, observability.ContentStats, error) {
	file, err := os.CreateTemp("", "axiom-content-export-*.jsonl")
	if err != nil {
		return nil, observability.ContentStats{}, err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		discard(file)
		removeTemp(path)
		return nil, observability.ContentStats{}, err
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	stats, err := source.Stream(ctx, q, func(m observability.ContentMeta) error {
		line, err := jsonx.MarshalCanonical(contentManifestEntry{
			RequestID: m.RequestID.String(),
			Kind:      string(m.Kind),
			SiteID:    m.Site.String(),
			AccountID: m.Account.String(),
			BlobID:    m.Blob.String(),
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339Nano),
			Size:      m.Size,
			Digest:    hex.EncodeToString(m.Digest[:]),
			ObjectKey: m.ObjectKey,
			Path:      m.TarPath(),
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
		return nil, observability.ContentStats{}, err
	}
	return file, stats, nil
}

// writeContentExport writes the tar.gz stream through the owner's encryption. The plaintext archive never touches the
// filesystem: only the encrypted output file the caller created does.
func writeContentExport(ctx context.Context, dst io.Writer, approval contentApproval, raw []byte, digest [32]byte,
	recipient *ecdh.PublicKey, meta *os.File, stats observability.ContentStats, reader contentOpener, now time.Time,
) error {
	encrypted, err := newExportWriter(dst, recipient, digest)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(encrypted)
	tw := tar.NewWriter(gz)
	if err := writeExportPrelude(tw, approval, raw, digest, stats, meta, now); err != nil {
		return err
	}
	if err := writeExportBodies(ctx, tw, meta, reader); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("content export: finish tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("content export: finish gzip: %w", err)
	}
	if err := encrypted.Close(); err != nil {
		return fmt.Errorf("content export: finish encryption: %w", err)
	}
	return nil
}

func writeExportPrelude(tw *tar.Writer, approval contentApproval, raw []byte, digest [32]byte,
	stats observability.ContentStats, meta *os.File, now time.Time,
) error {
	if err := writeTarBytes(tw, "approval.json", raw, now); err != nil {
		return err
	}
	manifest, err := jsonx.MarshalCanonical(map[string]any{
		"version":         1,
		"approval_digest": hex.EncodeToString(digest[:]),
		"owner_key":       approval.Owner,
		"recipient_key":   approval.Recipient,
		"since":           approval.Since,
		"until":           approval.Until,
		"site_id":         approval.Site,
		"account_id":      approval.Account,
		"destination":     approval.Destination,
		"format":          approval.Format,
		"content_count":   stats.Count,
		"content_bytes":   stats.Bytes,
		"created_at":      now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("content export: manifest: %w", err)
	}
	if err := writeTarBytes(tw, "manifest.json", manifest, now); err != nil {
		return err
	}
	return writeManifestEntries(tw, meta, now)
}

func writeManifestEntries(tw *tar.Writer, meta *os.File, now time.Time) error {
	info, err := meta.Stat()
	if err != nil {
		return fmt.Errorf("content export: metadata file: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.jsonl", Mode: 0o600, Size: info.Size(), ModTime: now.UTC()}); err != nil {
		return fmt.Errorf("content export: manifest header: %w", err)
	}
	if _, err := io.Copy(tw, meta); err != nil {
		return fmt.Errorf("content export: manifest entries: %w", err)
	}
	if _, err := meta.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("content export: rewind metadata: %w", err)
	}
	return nil
}

func writeExportBodies(ctx context.Context, tw *tar.Writer, meta *os.File, reader contentOpener) error {
	scanner := bufio.NewScanner(meta)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if err := writeExportBody(ctx, tw, scanner.Bytes(), reader); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("content export: metadata entries: %w", err)
	}
	return nil
}

func writeExportBody(ctx context.Context, tw *tar.Writer, line []byte, reader contentOpener) error {
	var entry contentManifestEntry
	if err := jsonx.UnmarshalKnown(line, &entry); err != nil {
		return fmt.Errorf("content export: metadata entry: %w", err)
	}
	m, err := entry.meta()
	if err != nil {
		return err
	}
	plaintext, err := reader.Open(ctx, m)
	if err != nil {
		return err
	}
	if int64(len(plaintext)) != m.Size {
		return fmt.Errorf("content export: %s is %d bytes, the manifest says %d", m.TarPath(), len(plaintext), m.Size)
	}
	if err := tw.WriteHeader(&tar.Header{Name: m.TarPath(), Mode: 0o600, Size: m.Size, ModTime: m.CreatedAt.UTC()}); err != nil {
		return fmt.Errorf("content export: header for %s: %w", m.TarPath(), err)
	}
	if _, err := tw.Write(plaintext); err != nil {
		return fmt.Errorf("content export: write %s: %w", m.TarPath(), err)
	}
	return nil
}

//nolint:cyclop // one validation and one parse per manifest field; splitting would hide the required set.
func (e contentManifestEntry) meta() (observability.ContentMeta, error) {
	var m observability.ContentMeta
	var err error
	if m.RequestID, err = uuid.Parse(e.RequestID); err != nil {
		return m, fmt.Errorf("content export: request id: %w", err)
	}
	if m.Site, err = uuid.Parse(e.SiteID); err != nil {
		return m, fmt.Errorf("content export: site id: %w", err)
	}
	if m.Account, err = uuid.Parse(e.AccountID); err != nil {
		return m, fmt.Errorf("content export: account id: %w", err)
	}
	if m.Blob, err = uuid.Parse(e.BlobID); err != nil {
		return m, fmt.Errorf("content export: blob id: %w", err)
	}
	m.Kind = observability.ContentKind(e.Kind)
	if m.Kind != observability.ContentRequest && m.Kind != observability.ContentResponse && m.Kind != observability.ContentAttempt {
		return m, fmt.Errorf("content export: unknown kind %q", e.Kind)
	}
	if m.CreatedAt, err = time.Parse(time.RFC3339Nano, e.CreatedAt); err != nil {
		return m, fmt.Errorf("content export: created_at: %w", err)
	}
	if e.Size <= 0 || e.Size > observability.MaxContentBytes {
		return m, fmt.Errorf("content export: size %d", e.Size)
	}
	m.Size = e.Size
	digest, err := hex.DecodeString(e.Digest)
	if err != nil || len(digest) != len(m.Digest) {
		return m, errors.New("content export: digest is not 32 hex-encoded bytes")
	}
	copy(m.Digest[:], digest)
	if e.ObjectKey == "" {
		return m, errors.New("content export: empty object key")
	}
	m.ObjectKey = e.ObjectKey
	return m, nil
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, now time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: now.UTC()}); err != nil {
		return fmt.Errorf("content export: header for %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("content export: write %s: %w", name, err)
	}
	return nil
}

// beginContentExport consumes the approval and records the attempt before any body is read. The unique digest is what
// makes an approval single-use; a replay loses the race at this insert and never reaches the object store.
