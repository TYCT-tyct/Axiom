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
	"syscall"
	"time"

	"axiom/internal/platform/jsonx"
)

// The call log's write-ahead spill. A Commit appends the record to a segment file and fsyncs it before it returns, so a
// record the caller was told is kept survives the process and the host: the writer reads the segments in append order,
// writes them to the database in batches, and deletes a segment only after the batch that covered it committed and the
// position was made durable. A crash during an append can leave only a short header or body at the end of the last
// segment; opening the spill drops that, because Commit never returned for it. A complete record that fails its
// checksum is damage and refuses startup instead (spill_open.go).
//
// One process owns a directory: opening it takes an exclusive lock and a second process is refused, because two writers
// appending to the same segment would overwrite each other without either noticing. Nodes therefore never share a spill
// directory (docs/domains/observability.md「写入路径」).
//
// The spill is bounded by the number of records admitted but not yet written (Recorder.Capacity). A record's encoding is
// capped at maxRecordBytes, so the disk bound is Capacity*(maxRecordBytes+recordHeaderBytes) plus one active segment.
// When the next record would exceed the bound, Commit returns ErrBacklog and the gateway refuses new calls instead of
// serving one it cannot record.
const (
	spillHeader       = "AXIOM-WAL-1\n"
	spillPrefix       = "call-"
	spillSuffix       = ".wal"
	spillLockName     = "call-log.lock"
	spillPosName      = "call-log.pos"
	spillPosHeader    = "AXIOM-WAL-POS-1\n"
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
	// errSpillLocked is a spill directory another process holds. Starting anyway would let two processes append from
	// the same offset and overwrite each other's records, so the second node refuses to start.
	errSpillLocked = errors.New("observability: the call log spill directory is in use by another process")
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

// position is a point in the spill: a segment and the offset after a record in it. The writer acknowledges a batch with
// one, the spill records it durably, and it deletes every complete segment before it.
type position struct {
	seq    uint64
	offset int64
}

// covers reports whether p is at or after q.
func (p position) covers(q position) bool {
	return p.seq > q.seq || (p.seq == q.seq && p.offset >= q.offset)
}

// syncWaiter is one Commit waiting for its record's fsync. The leader syncs the active segment and releases every
// waiter the sync covered.
type syncWaiter struct {
	pos  position
	done chan error
}

type spill struct {
	dir      string
	maxBytes int64
	lock     *os.File // held for the life of the spill; closing it lets the next process take the directory

	mu       sync.Mutex
	segments []*spillSegment // oldest first, including the active one
	active   *spillSegment
	// synced is how much of the active segment is durable; every segment before it is durable whole. The writer reads
	// no further: a record it stored and checkpointed past before its fsync could vanish in a power loss, leaving a
	// checkpoint beyond the segment's end that the next start must refuse.
	synced     int64
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

	// syncMu guards the group commit: one Commit fsyncs the segment that received its record, and every other Commit
	// that appended before that fsync shares its result instead of paying another.
	syncMu      sync.Mutex
	syncing     bool
	waiters     []syncWaiter
	durable     position
	haveDurable bool
}

// lockDir takes the directory's exclusive lock. flock is released by the kernel when the process dies, so a crashed
// node never leaves its spill locked; a second live process is refused instead of appending over the first.
func lockDir(dir string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(dir, spillLockName), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // dir is the operator's AXIOM_CALL_LOG_DIR
	if err != nil {
		return nil, fmt.Errorf("observability: open call log spill lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeQuietly(file)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", errSpillLocked, dir)
		}
		return nil, fmt.Errorf("observability: lock call log spill: %w", err)
	}
	return file, nil
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
	s.synced = segment.size
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

// append writes one encoded record and returns its position. It does not sync: Commit calls sync before it returns, so
// the bytes survive a host power loss by then.
func (s *spill) append(record []byte) (position, error) {
	if len(record) > maxRecordBytes {
		return position{}, errRecordTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken != nil {
		return position{}, s.broken
	}
	if s.closed || (s.active != nil && s.active.file == nil) {
		return position{}, ErrClosed
	}
	need := int64(recordHeaderBytes + len(record))
	if err := s.ensureSegmentLocked(need); err != nil {
		return position{}, err
	}
	if s.totalBytes+need > s.maxBytes {
		return position{}, errSpillFull
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
			return position{}, resetErr
		}
		s.broken = err
		return position{}, fmt.Errorf("observability: write call log segment: %w", err)
	}
	s.active.size += need
	s.totalBytes += need
	s.pending++
	s.notify()
	return position{seq: s.active.seq, offset: s.active.size}, nil
}

// appendSynced appends one record and makes it durable before returning. Commit calls it: once it returns, the record
// survives a host power loss, not only a process crash (docs/domains/observability.md「写入路径」).
func (s *spill) appendSynced(record []byte) error {
	pos, err := s.append(record)
	if err != nil {
		return err
	}
	return s.sync(pos)
}

// sync makes pos durable. Commits that appended while another fsync was running share that fsync: the leader syncs the
// active segment and releases every waiter the segment now covers.
func (s *spill) sync(pos position) error {
	s.syncMu.Lock()
	if s.haveDurable && s.durable.covers(pos) {
		s.syncMu.Unlock()
		return nil
	}
	done := make(chan error, 1)
	s.waiters = append(s.waiters, syncWaiter{pos: pos, done: done})
	leader := !s.syncing
	if leader {
		s.syncing = true
	}
	s.syncMu.Unlock()
	if leader {
		s.runSyncs()
	}
	return <-done
}

// runSyncs is the group-commit leader: it fsyncs until every registered waiter is covered or the sync fails. A waiter
// that arrived during the fsync is covered by the next round.
func (s *spill) runSyncs() {
	for {
		end, err := s.syncActive()
		s.syncMu.Lock()
		if err == nil {
			s.durable, s.haveDurable = end, true
		}
		remaining := s.waiters[:0]
		for _, w := range s.waiters {
			if err != nil || s.durable.covers(w.pos) {
				w.done <- err
				continue
			}
			remaining = append(remaining, w)
		}
		s.waiters = remaining
		if len(remaining) == 0 {
			s.syncing = false
			s.syncMu.Unlock()
			return
		}
		s.syncMu.Unlock()
	}
}

// syncActive fsyncs the active segment and returns the position it made durable. It releases the append lock before
// the fsync: records that arrive during it append normally and are covered by the next round, which is what lets one
// fsync serve a whole group of concurrent Commits. Rotation syncs a segment before it closes, so a waiter whose
// segment is already behind the active one is covered by that rotation.
func (s *spill) syncActive() (position, error) {
	s.mu.Lock()
	if s.active == nil || s.active.file == nil {
		s.mu.Unlock()
		return position{}, ErrClosed
	}
	file, end := s.active.file, position{seq: s.active.seq, offset: s.active.size}
	s.mu.Unlock()
	if err := file.Sync(); err != nil {
		// A rotation may have closed the segment after we captured it. It synced the segment before closing, so the
		// record is durable even though this handle lost the race; any other error is a real sync failure.
		s.mu.Lock()
		rotated := s.active != nil && s.active.seq != end.seq
		s.mu.Unlock()
		if rotated && errors.Is(err, os.ErrClosed) {
			return end, nil
		}
		// After a failed fsync the kernel may have dropped the pages, and a later fsync can succeed without them: the
		// segment can no longer be trusted, so the spill stops taking and reading records.
		err = fmt.Errorf("observability: sync call log segment: %w", err)
		s.mu.Lock()
		if s.broken == nil {
			s.broken = err
		}
		s.mu.Unlock()
		s.notify()
		return position{}, err
	}
	s.mu.Lock()
	if s.active != nil && s.active.seq == end.seq && end.offset > s.synced {
		s.synced = end.offset
	}
	s.mu.Unlock()
	s.notify() // the writer reads only what is durable
	return end, nil
}

// readableLocked is how far the writer may read seg: the durable part of the active segment, any other whole.
func (s *spill) readableLocked(seg *spillSegment) int64 {
	if seg == s.active {
		return s.synced
	}
	return seg.size
}

// awaitingSyncLocked reports records appended to a healthy spill whose fsync has not finished yet: a draining writer
// waits for them instead of exiting before they are readable.
func (s *spill) awaitingSyncLocked() bool {
	return s.broken == nil && s.active != nil && s.active.file != nil && s.active.size > s.synced
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

// finish closes the files the writer borrowed and releases the directory lock. Records left in a segment stay on disk.
func (s *spill) finish() {
	s.mu.Lock()
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
	if s.lock != nil {
		closeQuietly(s.lock)
		s.lock = nil
	}
	s.mu.Unlock()
	// No Commit may keep waiting for a sync whose file is gone.
	s.syncMu.Lock()
	for _, w := range s.waiters {
		w.done <- ErrClosed
	}
	s.waiters = nil
	s.syncing = false
	s.syncMu.Unlock()
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
		if s.stopped() || (s.closed && !s.awaitingSyncLocked()) {
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

// ack records that the batch ending at end committed. It first makes that position durable in the spill's checkpoint,
// then frees every complete segment before it; the active segment stays because it may still receive records. An
// acknowledged segment with no unacknowledged records behind it is rotated away even when it never filled, so a quiet
// node does not keep months of already-stored records in its active segment. A failed checkpoint write leaves the
// segments for the next ack: replaying them is harmless because the inserts are idempotent.
func (s *spill) ack(end position, records int) error {
	s.mu.Lock()
	s.pending -= int64(records)
	if s.pending < 0 {
		s.pending = 0
	}
	if s.pending == 0 && !s.closed && !s.stopped() && s.active != nil &&
		s.active.size > int64(len(spillHeader)) {
		if err := s.rotateLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.mu.Unlock()
	if err := s.writeCheckpoint(end); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reclaimLocked(end)
	return nil
}

// reclaimLocked deletes every segment entirely covered by end. The active segment is never deleted: it may still
// receive records.
func (s *spill) reclaimLocked(end position) {
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

// writeCheckpoint makes the acknowledged position durable. It is written to a temporary file and renamed, so a crash
// leaves either the old position or the new one, never a partial value that would skip a record.
func (s *spill) writeCheckpoint(pos position) error {
	tmp, err := os.CreateTemp(s.dir, ".pos-")
	if err != nil {
		return fmt.Errorf("observability: create call log checkpoint: %w", err)
	}
	name := tmp.Name()
	defer removeQuietly(name)
	if _, err := fmt.Fprintf(tmp, "%s%d %d\n", spillPosHeader, pos.seq, pos.offset); err != nil {
		closeQuietly(tmp)
		return fmt.Errorf("observability: write call log checkpoint: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		closeQuietly(tmp)
		return fmt.Errorf("observability: sync call log checkpoint: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("observability: close call log checkpoint: %w", err)
	}
	if err := os.Rename(name, filepath.Join(s.dir, spillPosName)); err != nil {
		return fmt.Errorf("observability: replace call log checkpoint: %w", err)
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("observability: open call log directory: %w", err)
	}
	defer closeQuietly(dir)
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("observability: sync call log directory: %w", err)
	}
	return nil
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
