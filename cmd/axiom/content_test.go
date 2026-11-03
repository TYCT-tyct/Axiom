package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/jsonx"
)

type fixedContentSource struct {
	metas []observability.ContentMeta
}

func (f fixedContentSource) Stream(_ context.Context, _ observability.ContentQuery, each func(observability.ContentMeta) error) (observability.ContentStats, error) {
	var stats observability.ContentStats
	for _, m := range f.metas {
		if err := each(m); err != nil {
			return observability.ContentStats{}, err
		}
		stats.Count++
		stats.Bytes += m.Size
	}
	return stats, nil
}

type fixedContentOpener map[string][]byte

func (f fixedContentOpener) Open(_ context.Context, m observability.ContentMeta) ([]byte, error) {
	data, ok := f[m.ObjectKey]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func contentTestApproval(t *testing.T, now time.Time, recipient []byte) (contentApproval, []byte, [32]byte, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApproval(now, time.Hour, now.Add(-24*time.Hour), now, uuid.NewV7(), uuid.UUID{}, "/tmp/export.tar.gz.enc", fingerprint(recipient))
	if err != nil {
		t.Fatal(err)
	}
	a.Owner = fingerprint(pub)
	raw, digest, err := signContentApproval(priv.Seed(), a)
	if err != nil {
		t.Fatal(err)
	}
	return a, raw, digest, pub
}

//nolint:cyclop,gocognit // one approval through every refusal.
func TestContentApprovalVerifies(t *testing.T) {
	now := time.Now().UTC()
	encryption, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval, raw, digest, signing := contentTestApproval(t, now, encryption.PublicKey().Bytes())
	got, gotRaw, gotDigest, err := verifyContentApproval(raw, signing, encryption.PublicKey().Bytes(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.Destination != approval.Destination || got.Owner != approval.Owner || !bytes.Equal(gotRaw, raw) || gotDigest != digest {
		t.Fatalf("verified %+v, want %+v", got, approval)
	}

	t.Run("tampered scope", func(t *testing.T) {
		changed := bytes.Replace(raw, []byte("/tmp/export"), []byte("/tmp/evil"), 1)
		if _, _, _, err := verifyContentApproval(changed, signing, encryption.PublicKey().Bytes(), now); err == nil {
			t.Fatal("a changed destination verified")
		}
	})
	t.Run("other signing key", func(t *testing.T) {
		other, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := verifyContentApproval(raw, other, encryption.PublicKey().Bytes(), now); err == nil {
			t.Fatal("another signing key verified")
		}
	})
	t.Run("other recipient", func(t *testing.T) {
		other, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := verifyContentApproval(raw, signing, other.PublicKey().Bytes(), now); err == nil {
			t.Fatal("another recipient verified")
		}
	})
	t.Run("expired", func(t *testing.T) {
		if _, _, _, err := verifyContentApproval(raw, signing, encryption.PublicKey().Bytes(), now.Add(2*time.Hour)); err == nil {
			t.Fatal("an expired approval verified")
		}
	})
	t.Run("unknown approval field", func(t *testing.T) {
		changed := bytes.Replace(raw, []byte(`"action":"content.export"`), []byte(`"action":"content.export","extra":"x"`), 1)
		if _, _, _, err := verifyContentApproval(changed, signing, encryption.PublicKey().Bytes(), now); err == nil {
			t.Fatal("an approval with an unknown field verified")
		}
	})
}

//nolint:cyclop,gocognit // key generation, approval and its refusals are one journey.
func TestContentKeygenAndApprove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "owner")
	var out bytes.Buffer
	if err := contentKeygenCommand([]string{"--dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner-signing.key", "owner-signing.pub", "owner-encryption.key", "owner-encryption.pub"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o644)
		if strings.HasSuffix(name, ".key") {
			want = 0o600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s has mode %o, want %o", name, info.Mode().Perm(), want)
		}
	}
	var keys map[string]any
	if err := jsonx.Unmarshal(out.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if keys["signing"] == nil || keys["encryption"] == nil || keys["dir"] != dir {
		t.Fatalf("keygen printed %v", keys)
	}
	if err := contentKeygenCommand([]string{"--dir", dir}, io.Discard); err == nil {
		t.Fatal("keygen overwrote existing keys")
	}

	approvalPath := filepath.Join(t.TempDir(), "approval.json")
	now := time.Now().UTC()
	if err := contentApproveCommand([]string{
		"--signing-key", filepath.Join(dir, "owner-signing.key"),
		"--encryption-key", filepath.Join(dir, "owner-encryption.pub"),
		"--since", now.Add(-time.Hour).Format(time.RFC3339),
		"--until", now.Format(time.RFC3339),
		"--destination", "/tmp/export.tar.gz.enc",
		"--out", approvalPath,
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(approvalPath) // #nosec G304 -- a path under the test's temp directory
	if err != nil {
		t.Fatal(err)
	}
	signingPub, err := os.ReadFile(filepath.Join(dir, "owner-signing.pub")) // #nosec G304 -- a path under the test's temp directory
	if err != nil {
		t.Fatal(err)
	}
	encryptionPub, err := os.ReadFile(filepath.Join(dir, "owner-encryption.pub")) // #nosec G304 -- a path under the test's temp directory
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := verifyContentApproval(raw, signingPub, encryptionPub, time.Now()); err != nil {
		t.Fatalf("the generated approval did not verify: %v", err)
	}
}

//nolint:cyclop,gocognit // one export through encryption, archive, decryption and tamper cases.
func TestContentExportRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	recipient, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval, raw, digest, _ := contentTestApproval(t, now, recipient.PublicKey().Bytes())
	first := []byte(`{"model":"claude","messages":["first body"]}`)
	second := []byte(`{"id":"msg_2","content":"second body"}`)
	m1 := observability.ContentMeta{
		RequestID: uuid.NewV7(), Kind: observability.ContentRequest, Site: uuid.NewV7(), Account: uuid.NewV7(),
		Blob: uuid.NewV7(), CreatedAt: now.Add(-time.Hour), ObjectKey: "object-one", Size: int64(len(first)),
	}
	m1.Digest = sha256.Sum256(first)
	m2 := observability.ContentMeta{
		RequestID: m1.RequestID, Kind: observability.ContentResponse, Site: m1.Site, Account: m1.Account,
		Blob: uuid.NewV7(), CreatedAt: now, ObjectKey: "object-two", Size: int64(len(second)),
	}
	m2.Digest = sha256.Sum256(second)
	meta, stats, err := prepareContentExport(t.Context(), fixedContentSource{metas: []observability.ContentMeta{m1, m2}},
		observability.ContentQuery{Since: now.Add(-2 * time.Hour), Until: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		discard(meta)
		removeTemp(meta.Name())
	}()
	var encrypted bytes.Buffer
	if err := writeContentExport(t.Context(), &encrypted, approval, raw, digest, recipient.PublicKey(), meta, stats,
		fixedContentOpener{"object-one": first, "object-two": second}, now); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Bytes(), first) || bytes.Contains(encrypted.Bytes(), second) {
		t.Fatal("the encrypted export contains a plaintext body")
	}

	var tarGz bytes.Buffer
	decrypted, err := decryptContentExport(&tarGz, bytes.NewReader(encrypted.Bytes()), recipient)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted.ApprovalDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("decrypted approval %s, want %x", decrypted.ApprovalDigest, digest)
	}
	files := readExportTar(t, tarGz.Bytes())
	if !bytes.Equal(files["approval.json"], raw) {
		t.Fatal("approval.json differs from the signed file")
	}
	var manifest struct {
		ContentCount int   `json:"content_count"`
		ContentBytes int64 `json:"content_bytes"`
	}
	if err := jsonx.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ContentCount != 2 || manifest.ContentBytes != int64(len(first)+len(second)) {
		t.Fatalf("manifest says %+v", manifest)
	}
	if !bytes.Equal(files["content/"+m1.RequestID.String()+"/request"], first) {
		t.Fatal("the request body differs")
	}
	if !bytes.Equal(files["content/"+m2.RequestID.String()+"/response"], second) {
		t.Fatal("the response body differs")
	}
	lines := bytes.Split(bytes.TrimSpace(files["manifest.jsonl"]), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("manifest.jsonl has %d lines", len(lines))
	}
	var entry contentManifestEntry
	if err := jsonx.UnmarshalKnown(lines[0], &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Path != "content/"+m1.RequestID.String()+"/request" || entry.ObjectKey != "object-one" {
		t.Fatalf("first manifest entry is %+v", entry)
	}

	t.Run("tampered", func(t *testing.T) {
		changed := append([]byte(nil), encrypted.Bytes()...)
		changed[len(changed)-20] ^= 1
		if _, err := decryptContentExport(io.Discard, bytes.NewReader(changed), recipient); err == nil {
			t.Fatal("a tampered export decrypted")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, err := decryptContentExport(io.Discard, bytes.NewReader(encrypted.Bytes()[:encrypted.Len()-1]), recipient); err == nil {
			t.Fatal("a truncated export decrypted")
		}
	})
	t.Run("other key", func(t *testing.T) {
		other, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decryptContentExport(io.Discard, bytes.NewReader(encrypted.Bytes()), other); err == nil {
			t.Fatal("another key decrypted the export")
		}
	})
}

func readExportTar(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close() //nolint:errcheck // read-only
	files := map[string][]byte{}
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = body
	}
	return files
}

func TestContentPublicKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readContentPublicKey(link); err == nil {
		t.Fatal("a symlink was accepted as a public key")
	}
}

func TestContentDestinationValidation(t *testing.T) {
	if err := validateContentDestination("/tmp/export.tar.gz.enc"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", "/tmp/../etc", "/tmp/a\x00b", "/tmp/a\nb"} {
		if err := validateContentDestination(path); err == nil {
			t.Fatalf("accepted destination %q", path)
		}
	}
}
