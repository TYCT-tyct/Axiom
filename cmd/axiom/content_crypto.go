package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	contentExportMagic      = "AXIOMCE1"
	contentExportVersion    = 1
	contentExportChunkSize  = 64 << 10
	contentExportTagSize    = 16
	contentExportHeaderSize = len(contentExportMagic) + 1 + 32 + 32 + 32 + 12
	contentExportInfo       = "axiom/content-export/v1"
)

// exportWriter encrypts a stream to the owner's X25519 key. One ephemeral key per export gives every file its own
// AES-256-GCM key; the header is the HKDF salt and the AAD of every chunk, so changing the recipient, the approval or
// a chunk's position fails authentication. A final chunk, possibly empty, marks a complete file; a truncated file has
// no valid final chunk.
type exportWriter struct {
	dst    io.Writer
	aead   cipher.AEAD
	header []byte
	base   [12]byte
	buf    []byte
	index  uint64
	closed bool
	err    error
}

// newExportWriter writes the header and returns a writer for the plaintext tar stream. The caller must Close it to
// write the final chunk.
func newExportWriter(dst io.Writer, recipient *ecdh.PublicKey, approval [32]byte) (*exportWriter, error) {
	if dst == nil || recipient == nil {
		return nil, errors.New("content export: no destination or recipient key")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("content export: ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("content export: recipient key: %w", err)
	}
	recipientPrint := sha256.Sum256(recipient.Bytes())
	base := make([]byte, 12)
	rand.Read(base)
	header := make([]byte, 0, contentExportHeaderSize)
	header = append(header, contentExportMagic...)
	header = append(header, contentExportVersion)
	header = append(header, ephemeral.PublicKey().Bytes()...)
	header = append(header, recipientPrint[:]...)
	header = append(header, approval[:]...)
	header = append(header, base...)
	key, err := hkdf.Key(sha256.New, shared, header, contentExportInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("content export: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("content export: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("content export: AEAD: %w", err)
	}
	if _, err := dst.Write(header); err != nil {
		return nil, fmt.Errorf("content export: write header: %w", err)
	}
	w := &exportWriter{dst: dst, aead: aead, header: header, buf: make([]byte, 0, contentExportChunkSize)}
	copy(w.base[:], base)
	return w, nil
}

func (w *exportWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("content export: write after close")
	}
	written := 0
	for len(p) > 0 {
		n := min(len(p), contentExportChunkSize-len(w.buf))
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		written += n
		if len(w.buf) == contentExportChunkSize {
			if err := w.flush(false); err != nil {
				w.err = err
				return written, err
			}
		}
	}
	return written, nil
}

// Close writes the final chunk. It is safe to call once; a second call reports the first result.
func (w *exportWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil {
		w.err = w.flush(true)
	}
	return w.err
}

func (w *exportWriter) flush(final bool) error {
	nonce := make([]byte, len(w.base))
	copy(nonce, w.base[:])
	binary.BigEndian.PutUint64(nonce[4:], w.index)
	flag := byte(0)
	if final {
		flag = 1
	}
	sealed := w.aead.Seal(nil, nonce, w.buf, exportChunkAAD(w.header, w.index, flag))
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed))) //nolint:gosec // chunk size plus tag fits in uint32
	if _, err := w.dst.Write([]byte{flag}); err != nil {
		return fmt.Errorf("content export: write chunk flag: %w", err)
	}
	if _, err := w.dst.Write(length[:]); err != nil {
		return fmt.Errorf("content export: write chunk length: %w", err)
	}
	if _, err := w.dst.Write(sealed); err != nil {
		return fmt.Errorf("content export: write chunk: %w", err)
	}
	w.buf = w.buf[:0]
	w.index++
	return nil
}

// exportDecryptStats is what the owner can check after decrypting: the approval the file is bound to and its size.
type exportDecryptStats struct {
	ApprovalDigest string
	Bytes          int64
}

// exportReader is the authenticated header and cipher state of one export file.
type exportReader struct {
	header   []byte
	approval []byte
	base     []byte
	aead     cipher.AEAD
}

// decryptContentExport verifies and decrypts a file to dst. It writes nothing to dst before a chunk authenticates, but
// a later failure can leave earlier chunks written; the command decrypts to a temporary file and publishes it only
// after this returns nil.
func decryptContentExport(dst io.Writer, src io.Reader, private *ecdh.PrivateKey) (exportDecryptStats, error) {
	if private == nil {
		return exportDecryptStats{}, errors.New("content export: no private key")
	}
	reader, err := openExportHeader(src, private)
	if err != nil {
		return exportDecryptStats{}, err
	}
	stats := exportDecryptStats{ApprovalDigest: hex.EncodeToString(reader.approval)}
	var index uint64
	for {
		flag, sealed, err := readExportChunk(src, index)
		if err != nil {
			return stats, err
		}
		plaintext, err := openExportChunk(reader, index, flag, sealed)
		if err != nil {
			return stats, err
		}
		if _, err := dst.Write(plaintext); err != nil {
			return stats, fmt.Errorf("content export: write plaintext: %w", err)
		}
		stats.Bytes += int64(len(plaintext))
		index++
		if flag == 1 {
			return stats, ensureExportEOF(src)
		}
	}
}

func openExportHeader(src io.Reader, private *ecdh.PrivateKey) (exportReader, error) {
	var zero exportReader
	header := make([]byte, contentExportHeaderSize)
	if _, err := io.ReadFull(src, header); err != nil {
		return zero, fmt.Errorf("content export: read header: %w", err)
	}
	if string(header[:len(contentExportMagic)]) != contentExportMagic {
		return zero, errors.New("content export: not an Axiom content export")
	}
	if header[len(contentExportMagic)] != contentExportVersion {
		return zero, fmt.Errorf("content export: version %d", header[len(contentExportMagic)])
	}
	offset := len(contentExportMagic) + 1
	ephemeralBytes := header[offset : offset+32]
	recipientPrint := header[offset+32 : offset+64]
	approval := header[offset+64 : offset+96]
	base := header[offset+96 : offset+108]
	own := sha256.Sum256(private.PublicKey().Bytes())
	if !hmac.Equal(recipientPrint, own[:]) {
		return zero, errors.New("content export: this file is for another encryption key")
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(ephemeralBytes)
	if err != nil {
		return zero, fmt.Errorf("content export: ephemeral key: %w", err)
	}
	shared, err := private.ECDH(ephemeral)
	if err != nil {
		return zero, fmt.Errorf("content export: shared secret: %w", err)
	}
	key, err := hkdf.Key(sha256.New, shared, header, contentExportInfo, 32)
	if err != nil {
		return zero, fmt.Errorf("content export: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return zero, fmt.Errorf("content export: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return zero, fmt.Errorf("content export: AEAD: %w", err)
	}
	return exportReader{header: header, approval: approval, base: base, aead: aead}, nil
}

func readExportChunk(src io.Reader, index uint64) (byte, []byte, error) {
	var flag [1]byte
	if _, err := io.ReadFull(src, flag[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil, errors.New("content export: file ends before a final chunk")
		}
		return 0, nil, fmt.Errorf("content export: read chunk flag: %w", err)
	}
	if flag[0] != 0 && flag[0] != 1 {
		return 0, nil, fmt.Errorf("content export: chunk %d has flag %d", index, flag[0])
	}
	var length [4]byte
	if _, err := io.ReadFull(src, length[:]); err != nil {
		return 0, nil, fmt.Errorf("content export: read chunk %d length: %w", index, err)
	}
	n := binary.BigEndian.Uint32(length[:])
	if n < contentExportTagSize || n > contentExportChunkSize+contentExportTagSize {
		return 0, nil, fmt.Errorf("content export: chunk %d has length %d", index, n)
	}
	if flag[0] == 0 && n != contentExportChunkSize+contentExportTagSize {
		return 0, nil, fmt.Errorf("content export: non-final chunk %d is not full", index)
	}
	sealed := make([]byte, n)
	if _, err := io.ReadFull(src, sealed); err != nil {
		return 0, nil, fmt.Errorf("content export: read chunk %d: %w", index, err)
	}
	return flag[0], sealed, nil
}

func openExportChunk(reader exportReader, index uint64, flag byte, sealed []byte) ([]byte, error) {
	nonce := make([]byte, len(reader.base))
	copy(nonce, reader.base)
	binary.BigEndian.PutUint64(nonce[4:], index)
	plaintext, err := reader.aead.Open(nil, nonce, sealed, exportChunkAAD(reader.header, index, flag))
	if err != nil {
		return nil, fmt.Errorf("content export: chunk %d failed authentication", index)
	}
	return plaintext, nil
}

func ensureExportEOF(src io.Reader) error {
	var extra [1]byte
	if got, err := src.Read(extra[:]); got != 0 {
		return errors.New("content export: data after the final chunk")
	} else if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("content export: after final chunk: %w", err)
	}
	return nil
}

func exportChunkAAD(header []byte, index uint64, final byte) []byte {
	aad := make([]byte, 0, len(header)+9)
	aad = append(aad, header...)
	aad = binary.BigEndian.AppendUint64(aad, index)
	return append(aad, final)
}
