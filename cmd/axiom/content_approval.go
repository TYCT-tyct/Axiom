package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/privfile"
)

const (
	contentApprovalVersion = "1"
	contentExportAction    = "content.export"
	contentExportFormat    = "tar.gz.x25519-aes256gcm"
	contentApprovalMaxAge  = 7 * 24 * time.Hour
	contentApprovalSkew    = 5 * time.Minute
	contentApprovalPrefix  = "axiom/content-export/approval/v1\n"

	maxApprovalBytes    = 64 << 10
	maxContentKeyBytes  = 32
	contentKeyFilePerm  = 0o600
	contentPubFilePerm  = 0o644
	contentApprovalPerm = 0o600
)

// contentApproval is what the owner signs. Every field is a string so one canonical encoding exists across tools;
// times are RFC 3339, identifiers are canonical UUIDs, and an empty site or account means every one.
type contentApproval struct {
	Action      string `json:"action"`
	Version     string `json:"version"`
	Owner       string `json:"owner"`
	Recipient   string `json:"recipient"`
	Since       string `json:"since"`
	Until       string `json:"until"`
	Site        string `json:"site"`
	Account     string `json:"account"`
	Destination string `json:"destination"`
	Format      string `json:"format"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at"`
	Nonce       string `json:"nonce"`
}

// contentApprovalFile is the file the operator hands to the server: the approval and the owner's signature over its
// canonical form. The signature is not a secret; the file can travel through the same channel as the command.
type contentApprovalFile struct {
	Approval  contentApproval `json:"approval"`
	Signature string          `json:"signature"`
}

// contentExportScope is the parsed, validated part of an approval the export query uses.
type contentExportScope struct {
	Since, Until  time.Time
	Site, Account uuid.UUID
}

// canonicalApproval returns the bytes covered by the owner's signature. A map makes the order explicit instead of
// depending on struct field order, so the owner's tool and the server agree even when one is written later.
func canonicalApproval(a contentApproval) ([]byte, error) {
	return jsonx.MarshalCanonical(map[string]string{
		"action":      a.Action,
		"version":     a.Version,
		"owner":       a.Owner,
		"recipient":   a.Recipient,
		"since":       a.Since,
		"until":       a.Until,
		"site":        a.Site,
		"account":     a.Account,
		"destination": a.Destination,
		"format":      a.Format,
		"issued_at":   a.IssuedAt,
		"expires_at":  a.ExpiresAt,
		"nonce":       a.Nonce,
	})
}

// signContentApproval fills the owner fingerprint and signs a with seed, returning the file to write and the digest
// the export row records. The seed never leaves the owner's machine.
func signContentApproval(seed []byte, a contentApproval) ([]byte, [32]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, [32]byte{}, fmt.Errorf("the signing key is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	private := ed25519.NewKeyFromSeed(seed)
	a.Owner = fingerprint(private.Public().(ed25519.PublicKey))
	canonical, err := canonicalApproval(a)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("approval: canonical encoding: %w", err)
	}
	signature := ed25519.Sign(private, append([]byte(contentApprovalPrefix), canonical...))
	digest := sha256.Sum256(canonical)
	file, err := jsonx.MarshalCanonical(contentApprovalFile{
		Approval:  a,
		Signature: base64.StdEncoding.EncodeToString(signature),
	})
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("approval: encode the file: %w", err)
	}
	return file, digest, nil
}

// verifyContentApproval authenticates data against the owner's public key and the recipient key, then checks that the
// approval is current and within the export's limits. It returns the approval, its raw file bytes and its digest.
func verifyContentApproval(data []byte, signing []byte, recipient []byte, now time.Time) (contentApproval, []byte, [32]byte, error) {
	var file contentApprovalFile
	if err := jsonx.UnmarshalKnown(data, &file); err != nil {
		return contentApproval{}, nil, [32]byte{}, fmt.Errorf("approval: %w", err)
	}
	if len(signing) != ed25519.PublicKeySize {
		return contentApproval{}, nil, [32]byte{}, errors.New("approval: the signing key is not an Ed25519 public key")
	}
	if len(recipient) != 32 {
		return contentApproval{}, nil, [32]byte{}, errors.New("approval: the encryption key is not an X25519 public key")
	}
	signature, err := base64.StdEncoding.DecodeString(file.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return contentApproval{}, nil, [32]byte{}, errors.New("approval: the signature is not Ed25519")
	}
	canonical, err := canonicalApproval(file.Approval)
	if err != nil {
		return contentApproval{}, nil, [32]byte{}, fmt.Errorf("approval: canonical encoding: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(signing), append([]byte(contentApprovalPrefix), canonical...), signature) {
		return contentApproval{}, nil, [32]byte{}, errors.New("approval: the owner's signature does not verify")
	}
	if want := fingerprint(signing); file.Approval.Owner != want {
		return contentApproval{}, nil, [32]byte{}, fmt.Errorf("approval: signed by %s, but it names %s", want, file.Approval.Owner)
	}
	if want := fingerprint(recipient); file.Approval.Recipient != want {
		return contentApproval{}, nil, [32]byte{}, fmt.Errorf("approval: for recipient %s, but this server holds %s", file.Approval.Recipient, want)
	}
	if _, err := file.Approval.scope(); err != nil {
		return contentApproval{}, nil, [32]byte{}, err
	}
	if err := file.Approval.validateTime(now); err != nil {
		return contentApproval{}, nil, [32]byte{}, err
	}
	return file.Approval, data, sha256.Sum256(canonical), nil
}

// validateTime checks the approval's own clock and its maximum life.
func (a contentApproval) validateTime(now time.Time) error {
	issued, err := time.Parse(time.RFC3339Nano, a.IssuedAt)
	if err != nil {
		return fmt.Errorf("approval: issued_at: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if err != nil {
		return fmt.Errorf("approval: expires_at: %w", err)
	}
	if !expires.After(issued) || expires.Sub(issued) > contentApprovalMaxAge {
		return errors.New("approval: expires_at must be after issued_at and within 7 days")
	}
	if now.Add(contentApprovalSkew).Before(issued) || !now.Before(expires) {
		return fmt.Errorf("approval: not valid at %s", now.UTC().Format(time.RFC3339))
	}
	return nil
}

// scope parses and validates the exported window and tenant.
func (a contentApproval) scope() (contentExportScope, error) {
	if a.Version != contentApprovalVersion || a.Action != contentExportAction || a.Format != contentExportFormat {
		return contentExportScope{}, errors.New("approval: version, action or format is not this command's")
	}
	since, err := time.Parse(time.RFC3339Nano, a.Since)
	if err != nil {
		return contentExportScope{}, fmt.Errorf("approval: since: %w", err)
	}
	until, err := time.Parse(time.RFC3339Nano, a.Until)
	if err != nil {
		return contentExportScope{}, fmt.Errorf("approval: until: %w", err)
	}
	if !until.After(since) || until.Sub(since) > observability.MaxContentExportRange {
		return contentExportScope{}, fmt.Errorf("approval: the window must be after since and at most %s", observability.MaxContentExportRange)
	}
	site, account, err := parseContentTenant(a.Site, a.Account)
	if err != nil {
		return contentExportScope{}, err
	}
	scope := contentExportScope{Since: since, Until: until, Site: site, Account: account}
	if err := validateContentDestination(a.Destination); err != nil {
		return contentExportScope{}, err
	}
	if err := validateApprovalNonce(a.Nonce); err != nil {
		return contentExportScope{}, err
	}
	return scope, nil
}

func parseContentTenant(siteText, accountText string) (uuid.UUID, uuid.UUID, error) {
	var site, account uuid.UUID
	var err error
	if siteText != "" {
		if site, err = uuid.Parse(siteText); err != nil {
			return uuid.UUID{}, uuid.UUID{}, fmt.Errorf("approval: site: %w", err)
		}
	}
	if accountText != "" {
		if account, err = uuid.Parse(accountText); err != nil {
			return uuid.UUID{}, uuid.UUID{}, fmt.Errorf("approval: account: %w", err)
		}
	}
	return site, account, nil
}

func validateContentDestination(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return errors.New("approval: destination must be a clean absolute path")
	}
	return nil
}

func validateApprovalNonce(text string) error {
	nonce, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(nonce) != 16 {
		return errors.New("approval: nonce must be 16 base64 bytes")
	}
	return nil
}

// fingerprint names a public key in the approval and the audit record.
func fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}

// readApprovalFile reads a small file, so a bad path cannot make the command read without bound.
func readApprovalFile(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- the operator names the approval file
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(file, maxApprovalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxApprovalBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxApprovalBytes)
	}
	return data, nil
}

// readContentPrivateKey opens one raw 32-byte private key, refusing a file the group or others can read.
func readContentPrivateKey(path string) ([]byte, error) {
	file, err := privfile.Open(path, os.Open)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read-only
	key, err := io.ReadAll(io.LimitReader(file, maxContentKeyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(key) != maxContentKeyBytes {
		return nil, fmt.Errorf("%s holds %d bytes, want a raw 32-byte key", path, len(key))
	}
	return key, nil
}

// readContentPublicKey opens a raw 32-byte public key. It refuses a file the group or others can write: a public key
// is not secret, but replacing it would let someone else authorize or decrypt an export.
func readContentPublicKey(path string) ([]byte, error) {
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if dir.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is in a directory writable by the group or others", path)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(path) // #nosec G304 -- the operator names the key file
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read-only
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	if after.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by the group or others", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxContentKeyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) != maxContentKeyBytes {
		return nil, fmt.Errorf("%s holds %d bytes, want a raw 32-byte key", path, len(data))
	}
	return data, nil
}

// newApproval builds the unsigned document an owner's `approve` verb signs.
func newApproval(now time.Time, ttl time.Duration, since, until time.Time, site, account uuid.UUID, destination string, recipient string) (contentApproval, error) {
	if ttl <= 0 || ttl > contentApprovalMaxAge {
		return contentApproval{}, fmt.Errorf("--ttl must be positive and at most %s", contentApprovalMaxAge)
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	a := contentApproval{
		Action:      contentExportAction,
		Version:     contentApprovalVersion,
		Recipient:   recipient,
		Since:       since.UTC().Format(time.RFC3339Nano),
		Until:       until.UTC().Format(time.RFC3339Nano),
		Destination: destination,
		Format:      contentExportFormat,
		IssuedAt:    now.UTC().Format(time.RFC3339Nano),
		ExpiresAt:   now.Add(ttl).UTC().Format(time.RFC3339Nano),
		Nonce:       base64.StdEncoding.EncodeToString(nonce),
	}
	if site != (uuid.UUID{}) {
		a.Site = site.String()
	}
	if account != (uuid.UUID{}) {
		a.Account = account.String()
	}
	if _, err := a.scope(); err != nil {
		return contentApproval{}, err
	}
	if err := a.validateTime(now); err != nil {
		return contentApproval{}, err
	}
	return a, nil
}
