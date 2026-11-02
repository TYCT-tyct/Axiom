package observability

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"axiom/internal/platform/jsonx"
)

// The call log's write-ahead spill. An Enqueue appends the record to a segment file before it returns, so a record the
// caller was told is kept survives the process that wrote it: the page cache outlives the process. The writer reads the
// segments in append order, writes them to the database in batches, and deletes a segment only after the batch that
// covered it committed. A record whose write was torn by a crash is the last one of the last segment; opening the spill
// drops it, because Enqueue never returned for it.
//
// The spill is bounded by the number of records admitted but not yet written (Recorder.Capacity). A record's encoding is
// capped at maxRecordBytes, so the disk bound is Capacity*(maxRecordBytes+recordHeaderBytes) plus one active segment.
// When the next record would exceed the bound, Enqueue returns ErrBacklog and the gateway refuses new calls instead of
// serving one it cannot record.
const (
	spillHeader       = "AXIOM-WAL-1\n"
	spillPrefix       = "call-"
	spillSuffix       = ".wal"
	spillSegmentBytes = 8 << 20
	maxRecordBytes    = 64 << 10
	recordHeaderBytes = 8
)

var (
	// errSpillFull is the spill's own bound. Admission reserves one record per call, so only a record larger than
	// maxRecordBytes or a spill whose cleanup cannot keep up reaches it.
	errSpillFull = errors.New("observability: the call log's spill is full")
	// errRecordTooLarge is a record whose encoding exceeds maxRecordBytes. It cannot be kept; the recorder marks
	// itself unrecordable so no further call is served without a record.
	errRecordTooLarge = errors.New("observability: the call record is larger than the spill allows")
)

// crcTable detects a torn or corrupted segment record. Castagnoli is in the standard library and has hardware support.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// closeQuietly closes a file whose error cannot change the outcome: the caller is already failing the operation or
// exiting, and there is nowhere to report a close failure.
func closeQuietly(file *os.File) {
	if file == nil {
		return
	}
	if err := file.Close(); err != nil {
		return
	}
}

// removeQuietly removes a file whose absence is not worth failing startup over; a leftover segment is replayed or
// rejected by the normal scan, never silently accepted.
func removeQuietly(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

// syncQuietly makes the active segment durable at normal shutdown. A failure leaves the records where they are; the
// next start still reads them, and Close reports them as pending.
func syncQuietly(file *os.File) {
	if err := file.Sync(); err != nil {
		return
	}
}

// resetAppend drops a failed or short write and puts the append cursor back on a record boundary.
func resetAppend(file *os.File, offset int64) error {
	if err := file.Truncate(offset); err != nil {
		return fmt.Errorf("observability: truncate after a failed call log write: %w", err)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("observability: seek after a failed call log write: %w", err)
	}
	return nil
}

// walRecord is one record as it rests in the spill: the two row shapes the insert statements take. It is JSON so a
// segment can be inspected and replayed with the same code that wrote it.
type walRecord struct {
	Call     row          `json:"call"`
	Attempts []attemptRow `json:"attempts,omitempty"`
}

func (w walRecord) queued() queued { return queued{call: w.Call, attempts: w.Attempts} }

// spillSegment is one append-only segment file. size includes the header and every complete record.
type spillSegment struct {
	seq  uint64
	path string
	size int64
	file *os.File // the append handle, only on the active segment
}

// position is the end of a batch in the spill: the segment and offset after its last record. The writer acknowledges a
// batch with it, and the spill deletes every complete segment before it.
type position struct {
	seq    uint64
	offset int64
}

type spill struct {
	dir      string
	maxBytes int64

	mu         sync.Mutex
	segments   []*spillSegment // oldest first, including the active one
	active     *spillSegment
	nextSeq    uint64
	totalBytes int64
	pending    int64 // records written and not yet acknowledged
	closed     bool  // no new records; the writer drains and exits
	broken     error // the spill cannot be read or written; admission refuses

	read       *spillSegment
	readFile   *os.File
	readOffset int64

	wake     chan struct{} // wakes the writer when a record arrives
	stop     chan struct{} // tells the writer to exit without draining, for a shutdown deadline
	stopOnce sync.Once
}

// createSegmentLocked creates the next segment and makes it active. The header is written before any record and the
// file is synced, so a later crash can tell a real segment from a file a crash left with no header.
func (s *spill) createSegmentLocked(seq uint64) error {
	path := filepath.Join(s.dir, spillPrefix+fmt.Sprintf("%016d", seq)+spillSuffix)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600) //nolint:gosec // path is the configured spill directory plus a validated sequence
	if err != nil {
		return fmt.Errorf("observability: create call log segment: %w", err)
	}
	if _, err := file.WriteString(spillHeader); err != nil {
		closeQuietly(file)
		removeQuietly(path)
		return fmt.Errorf("observability: write call log segment header: %w", err)
	}
	if err := file.Sync(); err != nil {
		closeQuietly(file)
		removeQuietly(path)
		return fmt.Errorf("observability: sync call log segment header: %w", err)
	}
	segment := &spillSegment{seq: seq, path: path, size: int64(len(spillHeader)), file: file}
	s.segments = append(s.segments, segment)
	s.active = segment
	s.totalBytes += int64(len(spillHeader))
	if seq >= s.nextSeq {
		s.nextSeq = seq + 1
	}
	return nil
}

// rotateLocked closes the active segment and starts the next one. The old segment is synced before it is closed: once
// the writer has acknowledged it, it will be deleted, and a power loss must not lose a record the database never saw.
func (s *spill) rotateLocked() error {
	if s.active != nil {
		if err := s.active.file.Sync(); err != nil {
			return fmt.Errorf("observability: sync call log segment: %w", err)
		}
		if err := s.active.file.Close(); err != nil {
			return fmt.Errorf("observability: close call log segment: %w", err)
		}
		s.active = nil
	}
	return s.createSegmentLocked(s.nextSeq)
}

// append writes one encoded record and returns after the page cache has it, so a process crash from this point on
// leaves the record in the segment. It does not fsync per record: the active segment is synced at rotation and before
// Close, so a host power loss can lose the newest records, which are exactly the ones not yet in the database.
func (s *spill) append(record []byte) error {
	if len(record) > maxRecordBytes {
		return errRecordTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken != nil {
		return s.broken
	}
	if s.closed || (s.active != nil && s.active.file == nil) {
		return ErrClosed
	}
	need := int64(recordHeaderBytes + len(record))
	if err := s.ensureSegmentLocked(need); err != nil {
		return err
	}
	if s.totalBytes+need > s.maxBytes {
		return errSpillFull
	}
	recordBytes := make([]byte, recordHeaderBytes+len(record))
	binary.BigEndian.PutUint32(recordBytes[:4], uint32(len(record))) //nolint:gosec // len(record) <= maxRecordBytes is checked above
	binary.BigEndian.PutUint32(recordBytes[4:recordHeaderBytes], crc32.Checksum(record, crcTable))
	copy(recordBytes[recordHeaderBytes:], record)
	before := s.active.size
	if _, err := s.active.file.Write(recordBytes); err != nil {
		// A short or failed write leaves a prefix that no reader can mistake for a record; remove it so the next
		// append starts at a complete boundary.
		if resetErr := resetAppend(s.active.file, before); resetErr != nil {
			s.broken = resetErr
			return resetErr
		}
		s.broken = err
		return fmt.Errorf("observability: write call log segment: %w", err)
	}
	s.active.size += need
	s.totalBytes += need
	s.pending++
	s.notify()
	return nil
}

// ensureSegmentLocked rotates when the active segment cannot hold the next record. A rotation failure makes the spill
// unrecordable: the old segment has already been closed, so later appends cannot pass it.
func (s *spill) ensureSegmentLocked(need int64) error {
	if s.active != nil && (s.active.size == int64(len(spillHeader)) || s.active.size+need <= spillSegmentBytes) {
		return nil
	}
	if err := s.rotateLocked(); err != nil {
		s.broken = err
		return err
	}
	return nil
}

// notify wakes the writer without blocking an Enqueue behind a full wake channel.
func (s *spill) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// close stops new appends and lets the writer drain what is already there.
func (s *spill) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.notify()
}

// stopNow ends the writer without draining, for a shutdown deadline or a test that simulates a crash. Records already
// in the segments stay there for the next start.
func (s *spill) stopNow() {
	s.stopOnce.Do(func() {
		// Close the stop channel first: a writer already finishing must see that the deadline passed and skip the
		// active segment's fsync instead of holding Close past it.
		close(s.stop)
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	})
	s.notify()
}

// stopped reports whether the writer was told to stop without draining.
func (s *spill) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// finish closes the files the writer borrowed. Records left in a segment stay on disk.
func (s *spill) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFile != nil {
		closeQuietly(s.readFile)
		s.readFile = nil
	}
	if s.active != nil && s.active.file != nil {
		// A normal drain makes the newest records durable before the process exits. A stop for a shutdown deadline or
		// a simulated crash skips it: the caller has already accepted that the records stay on disk for the next start,
		// and an fsync there could hold the deadline.
		if !s.stopped() {
			syncQuietly(s.active.file)
		}
		closeQuietly(s.active.file)
		s.active.file = nil
	}
}

// pendingCount is the number of records in the segments the database has not acknowledged.
func (s *spill) pendingCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// nextBatch returns up to max records in spill order, waiting for the first and then at most flush for the rest. It
// reports open=false when the spill is closed and drained (or stopping) and there is nothing left to write.
func (s *spill) nextBatch(max int, flush time.Duration) (batch []queued, end position, open bool, err error) {
	batch, end, err = s.firstBatch(max)
	if err != nil || len(batch) == 0 {
		return batch, end, false, err
	}
	if len(batch) >= max {
		return batch, end, true, nil
	}
	return s.fillBatch(batch, end, max, flush)
}

// firstBatch waits for a record and reads up to max of them, without waiting for a partial batch.
func (s *spill) firstBatch(max int) (batch []queued, end position, err error) {
	s.mu.Lock()
	for !s.availableLocked() {
		if s.closed || s.stopped() {
			broken := s.broken
			s.mu.Unlock()
			return nil, position{}, broken
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-s.stop:
			return nil, position{}, nil
		}
		s.mu.Lock()
	}
	batch, end, err = s.readLocked(max)
	s.mu.Unlock()
	return batch, end, err
}

// fillBatch waits up to flush for the rest of a partial batch.
func (s *spill) fillBatch(batch []queued, end position, max int, flush time.Duration) ([]queued, position, bool, error) {
	timer := time.NewTimer(flush)
	defer timer.Stop()
	for len(batch) < max {
		s.mu.Lock()
		more, next, readErr := s.readLocked(max - len(batch))
		closed, broken := s.closed, s.broken
		s.mu.Unlock()
		if readErr != nil {
			return batch, end, false, readErr
		}
		if len(more) > 0 {
			batch = append(batch, more...)
			end = next
			continue
		}
		if closed || broken != nil {
			// A closed spill still has every record after this batch to write; the writer loops until a call finds
			// nothing left. A broken one stops after this batch.
			return batch, end, broken == nil, broken
		}
		select {
		case <-s.wake:
		case <-timer.C:
			return batch, end, true, nil
		case <-s.stop:
			return batch, end, false, nil
		}
	}
	return batch, end, true, nil
}

// ack records that the batch ending at end committed. It frees every complete segment before end; the active segment
// stays because it may still receive records. A failed removal leaves the segment for the next ack: replaying it is
// harmless because the inserts are idempotent.
func (s *spill) ack(end position, records int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending -= int64(records)
	if s.pending < 0 {
		s.pending = 0
	}
	for len(s.segments) > 0 {
		segment := s.segments[0]
		if segment == s.active {
			return
		}
		if segment.seq > end.seq || (segment.seq == end.seq && end.offset < segment.size) {
			return
		}
		if err := os.Remove(segment.path); err != nil {
			return
		}
		if s.read == segment {
			if s.readFile != nil {
				closeQuietly(s.readFile)
				s.readFile = nil
			}
			s.read = nil
			s.readOffset = 0
		}
		s.totalBytes -= segment.size
		s.segments = s.segments[1:]
	}
}

// encodeRecord turns a validated record into the bytes one segment record carries.
func encodeRecord(call row, attempts []attemptRow) ([]byte, error) {
	body, err := jsonx.MarshalCanonical(walRecord{Call: call, Attempts: attempts})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errEncoding, err)
	}
	if len(body) > maxRecordBytes {
		return nil, errRecordTooLarge
	}
	return body, nil
}
