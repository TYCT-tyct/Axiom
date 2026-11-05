package observability

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// parking holds pending records (CallRecord.BillingPending) on disk outside the spill, one file per call, named by its
// request ID: a CRC32C of the record, then the record as the spill would hold it. A pending record is durable from its
// Commit, but it is not the call's record yet: the settled record that follows it replaces it, or, when no settlement
// will be written, the slot's Release moves it into the spill. Only a record in the spill reaches the database, so the
// call log is never written twice for one call and stays insert-only (README「写入」). A binary that predates parking
// does not look in this directory; its files wait for the next start of one that does.
type parking struct {
	root   *os.Root // the spill directory's pending/, which nothing here can name a path outside of
	dir    string
	sync   func(dir string) error
	logger *slog.Logger
}

const (
	parkDir    = "pending"
	parkSuffix = ".rec"
	parkTemp   = ".tmp"
)

// openParking makes dir/pending and its directory entry durable before any record can be parked in it.
func openParking(dir string, sync func(string) error, logger *slog.Logger) (parking, error) {
	p := parking{dir: filepath.Join(dir, parkDir), sync: sync, logger: logger}
	err := os.Mkdir(p.dir, 0o700)
	if errors.Is(err, os.ErrExist) {
		err = nil
	}
	if err == nil {
		err = sync(dir)
	}
	if err == nil {
		p.root, err = os.OpenRoot(p.dir)
	}
	if err != nil {
		return p, fmt.Errorf("observability: parked call records: %w", err)
	}
	return p, nil
}

// close releases the directory, on the way out: there is nothing left to do with a failure.
func (p parking) close() {
	if p.root == nil {
		return
	}
	if err := p.root.Close(); err != nil {
		p.logger.Warn("the parked call records' directory could not be closed", "err", err)
	}
}

func name(id string) string { return id + parkSuffix }

// park writes the record and makes it durable before returning: the file's bytes, then its name. A crash before the
// rename leaves a temporary file, which the next start removes; that Commit had not returned.
func (p parking) park(id string, record []byte) error {
	tmp := name(id) + parkTemp
	f, err := p.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	body := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(record)), crc32.Checksum(record, crcTable))
	_, err = f.Write(append(body, record...))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = p.root.Rename(tmp, name(id))
	}
	if err == nil {
		err = p.sync(p.dir)
	}
	if err != nil {
		p.remove(tmp)
	}
	return err
}

// unpark removes a parked record once the call's record is in the spill. A file a crash or a failed removal leaves
// behind is moved into the spill again at the next start, after that record, and the insert keeps the first.
func (p parking) unpark(id string) { p.remove(name(id)) }

func (p parking) remove(file string) {
	if err := p.root.Remove(file); err != nil {
		p.logger.Warn("a parked call record could not be removed; the next start replays it and the call log keeps the first",
			"file", file, "err", err)
	}
}

// names lists the parked records, or the temporary files of parks a crash interrupted.
func (p parking) names() ([]string, error) {
	d, err := p.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer closeQuietly(d)
	return d.Readdirnames(-1)
}

// recover moves every parked record into the spill, after the records already there, and removes it: the process that
// parked it is gone, so no settled record will replace it. A record whose bytes do not match their checksum fails the
// start, as a damaged segment does: it may be a call that was served.
func (p parking) recover(sp *spill) error {
	names, err := p.names()
	if err != nil {
		return fmt.Errorf("observability: parked call records: %w", err)
	}
	for _, file := range names {
		if strings.HasSuffix(file, parkTemp) {
			p.remove(file)
			continue
		}
		if !strings.HasSuffix(file, parkSuffix) {
			continue
		}
		path := filepath.Join(p.dir, file)
		b, err := p.root.ReadFile(file)
		if err != nil {
			return fmt.Errorf("observability: parked call record %s: %w", path, err)
		}
		if len(b) < 4 || crc32.Checksum(b[4:], crcTable) != binary.BigEndian.Uint32(b) {
			return fmt.Errorf("observability: parked call record %s: %w", path, errDamagedPark)
		}
		if err := sp.appendSynced(b[4:], true); err != nil {
			return fmt.Errorf("observability: parked call record %s: %w", path, err)
		}
		p.remove(file)
	}
	return nil
}

var errDamagedPark = errors.New("checksum mismatch")
