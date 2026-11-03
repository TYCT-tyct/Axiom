package observability

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ContentRequest, ContentResponse and ContentAttempt are the kinds of body one call can leave: the request, the
// response, or the raw upstream interaction. The writer that fills attempt comes with the capture path.
const (
	ContentRequest  ContentKind = "request"
	ContentResponse ContentKind = "response"
	ContentAttempt  ContentKind = "attempt"

	// MaxContentBytes bounds one stored body. A larger request or reply is split by the capture path before it reaches
	// the store; the export reads and decrypts one body at a time.
	MaxContentBytes = 64 << 20

	// MaxContentExportRange bounds one export's time range. It is a year so a training export can cover four quarters,
	// while an accidental open-ended query cannot walk every partition.
	MaxContentExportRange = 366 * 24 * time.Hour

	contentObjectVersion = 1
	contentObjectHeader  = 1 + 12 // version byte + AES-GCM nonce
	contentTagBytes      = 16
)

// ContentKind is where a stored body came from.
type ContentKind string

func (k ContentKind) valid() bool {
	return k == ContentRequest || k == ContentResponse || k == ContentAttempt
}

// ContentQuery selects the references one export covers: [Since, Until), optionally one site and one account. A zero
// Site or Account means every one.
type ContentQuery struct {
	Since, Until  time.Time
	Site, Account uuid.UUID
}

// ContentMeta is one reference and the blob it points at. It carries no body: the body is in the object store and is
// opened through ContentReader.
type ContentMeta struct {
	RequestID uuid.UUID
	Kind      ContentKind
	Site      uuid.UUID
	Account   uuid.UUID
	Blob      uuid.UUID
	CreatedAt time.Time
	ObjectKey string
	Digest    [32]byte
	Size      int64
}

// ContentStats is what a metadata scan covered.
type ContentStats struct {
	Count, Bytes int64
}

// ContentObjects is the part of platform/objectstore a content reader needs. A nil object store would make the export
// silently empty, so NewContentReader refuses it.
type ContentObjects interface {
	Get(ctx context.Context, key string) (*objectstore.Object, error)
}

// ErrNoContentKey means content references an account whose data key is gone. The body is unrecoverable by design;
// the export fails rather than skipping it.
var ErrNoContentKey = errors.New("observability: no content key for the account")

// StreamContentMeta walks the references in q in a stable order and hands each to each. The caller writes them to a
// temporary manifest inside the same read transaction, so the list is one snapshot and no content body is read while
// the transaction is open. It returns the count and plaintext bytes the caller must put in the manifest. Only request
// and response bodies are conversation content; attempt (raw upstream interaction) is not exported yet.
func StreamContentMeta(ctx context.Context, tx pg.Tx, q ContentQuery, each func(ContentMeta) error) (ContentStats, error) {
	if each == nil {
		return ContentStats{}, errors.New("observability: content export needs a callback")
	}
	if q.Since.IsZero() || !q.Until.After(q.Since) || q.Until.Sub(q.Since) > MaxContentExportRange {
		return ContentStats{}, errors.New("observability: a content export needs a range of at most 366 days")
	}
	rows, err := tx.Query(ctx, `
		SELECT r.request_id, r.kind, r.site_id, r.account_id, r.created_at,
		       b.id, b.object_key, b.digest, b.size
		FROM observability.content_refs r
		JOIN observability.content_blobs b
		  ON b.id = r.blob_id AND b.site_id = r.site_id AND b.account_id = r.account_id
		WHERE r.created_at >= $1 AND r.created_at < $2
		  AND r.kind IN ('request', 'response') -- attempt is upstream raw interaction, not conversation content yet
		  AND ($3::uuid IS NULL OR r.site_id = $3)
		  AND ($4::uuid IS NULL OR r.account_id = $4)
		ORDER BY r.created_at, r.account_id, r.request_id, r.kind`,
		q.Since, q.Until, optionalUUID(q.Site), optionalUUID(q.Account))
	if err != nil {
		return ContentStats{}, fmt.Errorf("observability: read content references: %w", err)
	}
	defer rows.Close()
	var stats ContentStats
	for rows.Next() {
		m, err := scanContentMeta(rows)
		if err != nil {
			return ContentStats{}, err
		}
		if err := addContentStats(&stats, m); err != nil {
			return ContentStats{}, err
		}
		if err := each(m); err != nil {
			return ContentStats{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return ContentStats{}, fmt.Errorf("observability: read content references: %w", err)
	}
	return stats, nil
}

// contentRowScanner is the part of pgx.Rows this package uses; naming it here keeps the pgx import out of the module.
type contentRowScanner interface {
	Scan(dest ...any) error
}

func scanContentMeta(row contentRowScanner) (ContentMeta, error) {
	var m ContentMeta
	var kind string
	var digest []byte
	var size int64
	if err := row.Scan(&m.RequestID, &kind, &m.Site, &m.Account, &m.CreatedAt, &m.Blob, &m.ObjectKey, &digest, &size); err != nil {
		return ContentMeta{}, fmt.Errorf("observability: read a content reference: %w", err)
	}
	m.Kind = ContentKind(kind)
	if !m.Kind.valid() {
		return ContentMeta{}, fmt.Errorf("observability: content reference %s has kind %q", m.RequestID, kind)
	}
	if len(digest) != len(m.Digest) {
		return ContentMeta{}, fmt.Errorf("observability: content reference %s has a %d-byte digest", m.RequestID, len(digest))
	}
	copy(m.Digest[:], digest)
	if size <= 0 || size > MaxContentBytes {
		return ContentMeta{}, fmt.Errorf("observability: content reference %s has size %d", m.RequestID, size)
	}
	m.Size = size
	return m, nil
}

func addContentStats(stats *ContentStats, m ContentMeta) error {
	if stats.Bytes > math.MaxInt64-m.Size {
		return errors.New("observability: content export is larger than an int64")
	}
	stats.Count++
	stats.Bytes += m.Size
	return nil
}

// ContentKeySource opens one account's data key. The caller supplies it from a platform-scope transaction: this
// package never builds a scope, and the command is the only caller that has one.
type ContentKeySource interface {
	ContentKey(context.Context, uuid.UUID) ([32]byte, error)
}

// ContentReader opens bodies for the export. It caches each account's data key for the life of one command; the cache
// is guarded so a future caller may use it from more than one goroutine.
type ContentReader struct {
	keys    ContentKeySource
	objects ContentObjects

	mu      sync.Mutex
	account map[uuid.UUID][32]byte
}

// NewContentReader returns a reader, refusing a missing dependency: an export that silently skipped the object store
// would look successful while exporting nothing.
func NewContentReader(keys ContentKeySource, objects ContentObjects) (*ContentReader, error) {
	if keys == nil || objects == nil {
		return nil, errors.New("observability: a content reader needs a key source and an object store")
	}
	return &ContentReader{keys: keys, objects: objects, account: map[uuid.UUID][32]byte{}}, nil
}

// Open fetches, decrypts and authenticates the body of m. The caller gets the original bytes; the object store only
// ever holds ciphertext, and the digest binds the plaintext to the reference list.
func (r *ContentReader) Open(ctx context.Context, m ContentMeta) ([]byte, error) {
	if m.ObjectKey == "" || m.Size <= 0 || m.Size > MaxContentBytes {
		return nil, fmt.Errorf("observability: content reference %s has an invalid object or size", m.RequestID)
	}
	key, err := r.accountKey(ctx, m.Account)
	if err != nil {
		return nil, err
	}
	sealed, err := r.readObject(ctx, m)
	if err != nil {
		return nil, err
	}
	plaintext, err := openContent(key, sealed, m)
	if err != nil {
		return nil, fmt.Errorf("observability: open content %s/%s: %w", m.RequestID, m.Kind, err)
	}
	return plaintext, nil
}

func (r *ContentReader) readObject(ctx context.Context, m ContentMeta) ([]byte, error) {
	object, err := r.objects.Get(ctx, m.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("observability: read content object %s: %w", m.ObjectKey, err)
	}
	if object == nil || object.Body == nil {
		return nil, fmt.Errorf("observability: read content object %s: no body", m.ObjectKey)
	}
	defer object.Body.Close() //nolint:errcheck // a read-only body has nothing to flush
	limit := contentObjectBytes(m.Size)
	if object.Size < 0 || object.Size > limit {
		return nil, fmt.Errorf("observability: content object %s is %d bytes, want at most %d", m.ObjectKey, object.Size, limit)
	}
	sealed, err := io.ReadAll(io.LimitReader(object.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("observability: read content object %s: %w", m.ObjectKey, err)
	}
	if int64(len(sealed)) > limit {
		return nil, fmt.Errorf("observability: content object %s is longer than its reference", m.ObjectKey)
	}
	return sealed, nil
}

// accountKey returns the account's data key, reading it once per command. A missing key is a hard error: content that
// cannot be opened must fail the export rather than disappear from it.
func (r *ContentReader) accountKey(ctx context.Context, account uuid.UUID) ([32]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key, ok := r.account[account]; ok {
		return key, nil
	}
	key, err := r.keys.ContentKey(ctx, account)
	if err != nil {
		return [32]byte{}, err
	}
	r.account[account] = key
	return key, nil
}

// ReadContentKey opens the account's sealed data key. The caller must pass a platform-scope transaction; the AAD is
// rebuilt from the row's account and the table name, so a key copied to another account or table does not open.
func ReadContentKey(ctx context.Context, tx pg.Tx, keys *secret.Keyring, account uuid.UUID) ([32]byte, error) {
	var ciphertext, nonce, wrapped []byte
	var version uint32
	err := tx.QueryRow(ctx, `SELECT ciphertext, nonce, wrapped_dek, key_version
		FROM observability.content_keys WHERE account_id = $1`, account).
		Scan(&ciphertext, &nonce, &wrapped, &version)
	if errors.Is(err, pg.ErrNoRows) {
		return [32]byte{}, fmt.Errorf("%w: %s", ErrNoContentKey, account)
	}
	if err != nil {
		return [32]byte{}, fmt.Errorf("observability: read the content key of %s: %w", account, err)
	}
	opened, err := keys.Open(secret.Sealed{Ciphertext: ciphertext, Nonce: nonce, WrappedDEK: wrapped, KeyVersion: version},
		secret.Location{Table: "observability.content_keys", RowID: account.String(), Purpose: "content_key"})
	if err != nil {
		return [32]byte{}, fmt.Errorf("observability: open the content key of %s: %w", account, err)
	}
	raw := opened.Reveal()
	if len(raw) != 32 {
		return [32]byte{}, fmt.Errorf("observability: the content key of %s is %d bytes, want 32", account, len(raw))
	}
	var key [32]byte
	copy(key[:], raw)
	if key == ([32]byte{}) {
		return [32]byte{}, fmt.Errorf("observability: the content key of %s is all zeroes", account)
	}
	return key, nil
}

// contentKeys derives the encryption and digest subkeys from the account's data key. Using one key for both AES-GCM
// and HMAC would couple two different algorithms; separate labels keep them independent.
func contentKeys(root [32]byte) (enc, mac [32]byte) {
	encKey, err := hkdf.Key(sha256.New, root[:], nil, "axiom/content/encryption/v1", 32)
	if err != nil {
		panic(err) // a SHA-256 HKDF of 32 bytes cannot fail
	}
	macKey, err := hkdf.Key(sha256.New, root[:], nil, "axiom/content/digest/v1", 32)
	if err != nil {
		panic(err)
	}
	copy(enc[:], encKey)
	copy(mac[:], macKey)
	return enc, mac
}

// contentDigest is the keyed digest stored on the blob and recorded in the export manifest. It lets the reader reject
// tampered plaintext without storing another copy, and it lets the future writer deduplicate within the account.
func contentDigest(macKey [32]byte, plaintext []byte) [32]byte {
	mac := hmac.New(sha256.New, macKey[:])
	_, _ = mac.Write([]byte("axiom/content/digest/v1\x00"))
	_, _ = mac.Write(plaintext)
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

// contentAAD binds a body to its account, blob and digest. The blob is shared by every call that sent the same bytes,
// so the request and kind are not part of it; the manifest carries those.
func contentAAD(m ContentMeta) []byte {
	b := make([]byte, 0, len("axiom/content/object/v1\x00")+16+16+32)
	b = append(b, "axiom/content/object/v1\x00"...)
	b = append(b, m.Account[:]...)
	b = append(b, m.Blob[:]...)
	b = append(b, m.Digest[:]...)
	return b
}

// sealContent encrypts plaintext with the account's data key. m carries the digest the writer computed; a mismatch is
// a caller bug, not a condition to repair silently.
func sealContent(key [32]byte, plaintext []byte, m ContentMeta) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > MaxContentBytes || int64(len(plaintext)) != m.Size {
		return nil, fmt.Errorf("observability: content is %d bytes, want 1 to %d in the reference", len(plaintext), MaxContentBytes)
	}
	encKey, macKey := contentKeys(key)
	if contentDigest(macKey, plaintext) != m.Digest {
		return nil, errors.New("observability: content digest does not match the reference")
	}
	block, err := aes.NewCipher(encKey[:])
	if err != nil {
		return nil, fmt.Errorf("observability: content cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("observability: content AEAD: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	sealed := aead.Seal(nil, nonce, plaintext, contentAAD(m))
	out := make([]byte, 0, contentObjectHeader+len(sealed))
	out = append(out, contentObjectVersion)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// openContent authenticates and decrypts one object. It checks the plaintext length and the keyed digest before
// returning anything to the caller.
func openContent(key [32]byte, object []byte, m ContentMeta) ([]byte, error) {
	if len(object) < contentObjectHeader+contentTagBytes {
		return nil, errors.New("content object is truncated")
	}
	if object[0] != contentObjectVersion {
		return nil, fmt.Errorf("content object version %d", object[0])
	}
	encKey, macKey := contentKeys(key)
	block, err := aes.NewCipher(encKey[:])
	if err != nil {
		return nil, fmt.Errorf("content cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("content AEAD: %w", err)
	}
	nonce := object[1:contentObjectHeader]
	plaintext, err := aead.Open(nil, nonce, object[contentObjectHeader:], contentAAD(m))
	if err != nil {
		return nil, errors.New("content object failed authentication")
	}
	if int64(len(plaintext)) != m.Size {
		return nil, fmt.Errorf("content is %d bytes, the reference says %d", len(plaintext), m.Size)
	}
	digest := contentDigest(macKey, plaintext)
	if !hmac.Equal(digest[:], m.Digest[:]) {
		return nil, errors.New("content digest does not match the reference")
	}
	return plaintext, nil
}

// contentObjectBytes is the largest stored object for a body of plaintext bytes: version, nonce, ciphertext and tag.
func contentObjectBytes(size int64) int64 {
	return contentObjectHeader + size + contentTagBytes
}

// optionalUUID passes nil for the zero UUID, which PostgreSQL reads as "no filter".
func optionalUUID(id uuid.UUID) any {
	if id == (uuid.UUID{}) {
		return nil
	}
	return id
}

// TarPath is the export's entry name for this reference. All parts are canonical UUIDs or a closed kind, so a path
// traversal is impossible.
func (m ContentMeta) TarPath() string {
	return "content/" + m.RequestID.String() + "/" + string(m.Kind)
}
