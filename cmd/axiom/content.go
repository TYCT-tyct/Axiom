package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"uuid"
)

const contentUsage = `usage:
  axiom content keygen --dir DIR
  axiom content approve --signing-key FILE --second-signing-key FILE --encryption-key FILE --since RFC3339 --until RFC3339 [--site ID] [--account ID] --destination PATH --out FILE [--ttl 24h]
  axiom content export --approval FILE
  axiom content decrypt --key FILE --in FILE --out FILE`

const (
	contentSigningKeyVar       = "AXIOM_CONTENT_EXPORT_SIGNING_KEY"
	contentSecondSigningKeyVar = "AXIOM_CONTENT_EXPORT_SECOND_SIGNING_KEY"
	contentEncryptionKeyVar    = "AXIOM_CONTENT_EXPORT_ENCRYPTION_KEY"
	contentExportTimeoutVar    = "AXIOM_CONTENT_EXPORT_TIMEOUT"
	contentExportTimeout       = time.Hour
)

func contentCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(contentUsage)
	}
	switch args[0] {
	case "keygen":
		return contentKeygenCommand(args[1:], out)
	case "approve":
		return contentApproveCommand(args[1:], out)
	case "export":
		return contentExportCommand(ctx, environ, args[1:], out)
	case "decrypt":
		return contentDecryptCommand(args[1:], out)
	default:
		return fmt.Errorf("unknown verb %q\n%s", args[0], contentUsage)
	}
}

// contentKeygenCommand makes the owner's three key pairs. Two independent Ed25519 keys make export approval a
// two-step operation: compromising or losing one signing key is not enough. The private keys are raw 32-byte files,
// mode 0600; the public keys are what the server is given. Nothing printed can reconstruct a private key.
//
//nolint:cyclop,funlen // one key-generation and output transaction
func contentKeygenCommand(args []string, out io.Writer) error {
	var dir string
	if err := flags("content keygen", contentUsage, args, func(set *flag.FlagSet) {
		set.StringVar(&dir, "dir", "", "directory for the six key files")
	}); err != nil {
		return err
	}
	if dir == "" {
		return fmt.Errorf("--dir is required\n%s", contentUsage)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by the group or others", dir)
	}
	signingPub, signingPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate the signing key: %w", err)
	}
	secondPub, secondPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate the second signing key: %w", err)
	}
	encryption, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate the encryption key: %w", err)
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"owner-signing.key", signingPriv.Seed(), contentKeyFilePerm},
		{"owner-signing.pub", signingPub, contentPubFilePerm},
		{"owner-signing-2.key", secondPriv.Seed(), contentKeyFilePerm},
		{"owner-signing-2.pub", secondPub, contentPubFilePerm},
		{"owner-encryption.key", encryption.Bytes(), contentKeyFilePerm},
		{"owner-encryption.pub", encryption.PublicKey().Bytes(), contentPubFilePerm},
	}
	var created []string
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if err := writeNewFile(path, file.data, file.mode); err != nil {
			for _, name := range created {
				removeTemp(name)
			}
			return err
		}
		created = append(created, path)
	}
	return writeLine(out, map[string]any{
		"dir":            dir,
		"signing":        fingerprint(signingPub),
		"second_signing": fingerprint(secondPub),
		"encryption":     fingerprint(encryption.PublicKey().Bytes()),
	})
}

// contentApproveCommand signs an approval on the owner's machine. The server can verify it with the public signing
// keys and encrypt to the public encryption key; no private key ever reaches the server.
//
//nolint:funlen // one approval transaction
func contentApproveCommand(args []string, out io.Writer) error {
	var signingPath, secondSigningPath, encryptionPath, sinceText, untilText, siteText, accountText, destination, output string
	var ttl time.Duration
	if err := flags("content approve", contentUsage, args, func(set *flag.FlagSet) {
		set.StringVar(&signingPath, "signing-key", "", "the owner's first Ed25519 private key")
		set.StringVar(&secondSigningPath, "second-signing-key", "", "the owner's second Ed25519 private key")
		set.StringVar(&encryptionPath, "encryption-key", "", "the owner's X25519 public key")
		set.StringVar(&sinceText, "since", "", "the first instant included, RFC 3339")
		set.StringVar(&untilText, "until", "", "the first instant excluded, RFC 3339")
		set.StringVar(&siteText, "site", "", "only this site; empty means every site")
		set.StringVar(&accountText, "account", "", "only this account; empty means every account")
		set.StringVar(&destination, "destination", "", "the absolute path the export writes on the server")
		set.StringVar(&output, "out", "", "where to write the signed approval")
		set.DurationVar(&ttl, "ttl", 24*time.Hour, "how long the approval is valid, at most 7 days")
	}); err != nil {
		return err
	}
	if err := requireContentFlags(contentUsage, []contentFlag{
		{"--signing-key", signingPath},
		{"--second-signing-key", secondSigningPath},
		{"--encryption-key", encryptionPath},
		{"--since", sinceText},
		{"--until", untilText},
		{"--destination", destination},
		{"--out", output},
	}); err != nil {
		return err
	}
	signingSeed, err := readContentPrivateKey(signingPath)
	if err != nil {
		return err
	}
	secondSigningSeed, err := readContentPrivateKey(secondSigningPath)
	if err != nil {
		return err
	}
	encryptionPub, err := readContentPublicKey(encryptionPath)
	if err != nil {
		return err
	}
	if _, err := ecdh.X25519().NewPublicKey(encryptionPub); err != nil {
		return fmt.Errorf("--encryption-key: %w", err)
	}
	since, until, site, account, err := readContentScope(sinceText, untilText, siteText, accountText)
	if err != nil {
		return err
	}
	now := time.Now()
	approval, err := newApproval(now, ttl, since, until, site, account, destination, fingerprint(encryptionPub))
	if err != nil {
		return err
	}
	signed, file, digest, err := signContentApproval(signingSeed, secondSigningSeed, approval)
	if err != nil {
		return err
	}
	if err := writeNewFile(output, file, contentApprovalPerm); err != nil {
		return err
	}
	return writeLine(out, map[string]any{
		"approval":   output,
		"digest":     hex.EncodeToString(digest[:]),
		"owner":      signed.Owner,
		"owner2":     signed.Owner2,
		"recipient":  signed.Recipient,
		"expires_at": signed.ExpiresAt,
	})
}

// contentDecryptCommand decrypts on the owner's machine. It is local-only: no database or object store is opened, and
// the private encryption key never leaves the owner's control.
func contentDecryptCommand(args []string, out io.Writer) error {
	var keyPath, inPath, outPath string
	if err := flags("content decrypt", contentUsage, args, func(set *flag.FlagSet) {
		set.StringVar(&keyPath, "key", "", "the owner's X25519 private key")
		set.StringVar(&inPath, "in", "", "the encrypted export")
		set.StringVar(&outPath, "out", "", "where to write the decrypted tar.gz")
	}); err != nil {
		return err
	}
	if err := requireContentFlags(contentUsage, []contentFlag{{"--key", keyPath}, {"--in", inPath}, {"--out", outPath}}); err != nil {
		return err
	}
	privateBytes, err := readContentPrivateKey(keyPath)
	if err != nil {
		return err
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		return fmt.Errorf("the decryption key: %w", err)
	}
	input, err := os.Open(inPath) // #nosec G304 -- the owner names their own export
	if err != nil {
		return err
	}
	defer input.Close() //nolint:errcheck // read-only
	if err := checkContentDestination(outPath); err != nil {
		return err
	}
	temp, err := createContentExportTemp(outPath)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	stats, err := decryptContentExport(temp, input, private)
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		removeTemp(tempPath)
		return err
	}
	if err := publishNoReplace(tempPath, outPath); err != nil {
		removeTemp(tempPath)
		return err
	}
	return writeLine(out, map[string]any{
		"done":     "content decrypt",
		"output":   outPath,
		"bytes":    stats.Bytes,
		"approval": stats.ApprovalDigest,
	})
}

// readContentScope parses the approval window and optional tenant; empty site and account mean every one.
func readContentScope(sinceText, untilText, siteText, accountText string) (time.Time, time.Time, uuid.UUID, uuid.UUID, error) {
	since, err := time.Parse(time.RFC3339, sinceText)
	if err != nil {
		return time.Time{}, time.Time{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("--since: %w", err)
	}
	until, err := time.Parse(time.RFC3339, untilText)
	if err != nil {
		return time.Time{}, time.Time{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("--until: %w", err)
	}
	site, err := optionalUUIDFlag("--site", siteText)
	if err != nil {
		return time.Time{}, time.Time{}, uuid.UUID{}, uuid.UUID{}, err
	}
	account, err := optionalUUIDFlag("--account", accountText)
	if err != nil {
		return time.Time{}, time.Time{}, uuid.UUID{}, uuid.UUID{}, err
	}
	return since, until, site, account, nil
}

type contentFlag struct{ name, value string }

func requireContentFlags(usage string, values []contentFlag) error {
	for _, flag := range values {
		if flag.value == "" {
			return fmt.Errorf("%s is required\n%s", flag.name, usage)
		}
	}
	return nil
}

func optionalUUIDFlag(name, value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.UUID{}, nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%s: %w", name, err)
	}
	if id == (uuid.UUID{}) {
		return uuid.UUID{}, fmt.Errorf("%s: the all-zero UUID means no tenant and cannot be selected explicitly", name)
	}
	return id, nil
}
