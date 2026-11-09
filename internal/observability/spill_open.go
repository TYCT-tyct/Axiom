package observability

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// errTornTail is a record or header whose last write did not finish: a short header or a short body at the very end of
// the last segment. Only that can be proven to be a write that never returned, so only that is truncated; a complete
// record whose checksum does not match is corruption and refuses startup instead of silently dropping a record the
// database may not have seen.
var errTornTail = errors.New("observability: torn call log tail")

// segmentFile is a segment found in the spill directory, before it is opened.
type segmentFile struct {
	seq  uint64
	path string
}

// openSpill locks and opens dir, then validates every segment it finds, dropping a torn tail from the last one and
// starting after the last acknowledged position. The writer replays every record after the checkpoint, including
// records whose database write committed before the process died; the inserts are idempotent, so replaying them cannot
// duplicate a row. Before it returns, and so before any Commit, the directory chain is made durable (syncDirChain); sync
// is syncDir, or a test's observer.
func openSpill(dir string, capacity int, sync func(string) error) (*spill, error) {
	if dir == "" {
		return nil, errors.New("observability: the call log needs a spill directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // dir is the operator's AXIOM_CALL_LOG_DIR
		return nil, fmt.Errorf("observability: call log spill directory: %w", err)
	}
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*spill, error) {
		closeQuietly(lock)
		return nil, err
	}
	checkpoint, err := readCheckpoint(dir)
	if err != nil {
		return fail(err)
	}
	files, err := listSegments(dir)
	if err != nil {
		return fail(err)
	}
	s := &spill{
		dir: dir, lock: lock, maxBytes: int64(capacity)*(maxRecordBytes+recordHeaderBytes) + spillSegmentBytes,
		nextSeq: 1, wake: make(chan struct{}, 1), stop: make(chan struct{}), syncDir: sync,
	}
	if err := s.loadSegments(files, checkpoint); err != nil {
		return fail(err)
	}
	if err := s.openActive(checkpoint); err != nil {
		return fail(err)
	}
	if len(s.segments) > 0 {
		s.read = s.segments[0]
		s.readOffset = int64(len(spillHeader))
		if s.read.seq == checkpoint.seq {
			s.readOffset = checkpoint.offset
		}
		if s.readOffset > s.read.size {
			return fail(fmt.Errorf("observability: call log checkpoint %d/%d is past the end of %s",
				checkpoint.seq, checkpoint.offset, s.read.path))
		}
	}
	if err := syncDirChain(dir, sync); err != nil {
		closeQuietly(s.active.file)
		return fail(err)
	}
	return s, nil
}

// syncDirChain makes dir and every directory above it durable in its parent. It runs on every start, not only when
// this process created the directories: a crash between creating a segment or a directory and syncing it leaves a
// link that the next start sees in the page cache but that a later power loss could still remove.
func syncDirChain(dir string, sync func(string) error) error {
	d, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("observability: call log spill directory: %w", err)
	}
	for {
		if err := sync(d); err != nil {
			return err
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil
		}
		d = parent
	}
}

// listSegments reads the directory and returns the segment files in sequence order.
func listSegments(dir string) ([]segmentFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("observability: read call log spill: %w", err)
	}
	files := make([]segmentFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if seq, ok := spillSeq(entry.Name()); ok {
			files = append(files, segmentFile{seq: seq, path: filepath.Join(dir, entry.Name())})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].seq < files[j].seq })
	return files, nil
}

// loadSegments validates the files and counts the records the writer must replay: those at or after checkpoint. A
// segment entirely before it is dropped without being read: its records are already in the database, and replaying
// them after the month's partition has been archived would insert them into the default partition as duplicates.
func (s *spill) loadSegments(files []segmentFile, checkpoint position) error {
	for i, f := range files {
		if i > 0 && f.seq == files[i-1].seq {
			return fmt.Errorf("observability: two call log segments numbered %d", f.seq)
		}
		if f.seq < checkpoint.seq {
			removeQuietly(f.path)
			continue
		}
		from := int64(len(spillHeader))
		if f.seq == checkpoint.seq {
			from = checkpoint.offset
		}
		size, after, pending, err := scanSegment(f.path, i == len(files)-1, from)
		if err != nil {
			return err
		}
		if size == 0 {
			continue // scanSegment removed an empty or torn-header last segment
		}
		s.segments = append(s.segments, &spillSegment{seq: f.seq, path: f.path, size: size, pending: pending})
		s.totalBytes += size
		s.pending += after
	}
	return nil
}

// openActive opens the last segment for appends, or starts a fresh one when the last is full or fully acknowledged. A
// fully acknowledged segment is rotated away without waiting for it to fill: otherwise a quiet node would keep months
// of already-stored records in its active segment and replay them after a restart.
func (s *spill) openActive(checkpoint position) error {
	next := checkpoint.seq + 1
	if len(s.segments) == 0 {
		s.nextSeq = next
		return s.createSegmentLocked(next, false)
	}
	last := s.segments[len(s.segments)-1]
	if last.seq >= next {
		next = last.seq + 1
	}
	ackedToEnd := last.seq == checkpoint.seq && checkpoint.offset >= last.size
	if last.size >= spillSegmentBytes || ackedToEnd {
		if ackedToEnd {
			// The segment holds only acknowledged records; drop it now, or leave it for the next ack if the disk
			// refuses the removal. The reader starts after its checkpoint either way.
			if err := os.Remove(last.path); err == nil {
				s.totalBytes -= last.size
				s.segments = s.segments[:len(s.segments)-1]
			}
		}
		s.nextSeq = next
		return s.createSegmentLocked(next, false)
	}
	file, err := os.OpenFile(last.path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("observability: open call log segment: %w", err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		closeQuietly(file)
		return fmt.Errorf("observability: seek call log segment: %w", err)
	}
	last.file = file
	s.active = last
	s.synced = last.size // scanSegment synced the last segment
	s.nextSeq = next
	return nil
}

// readCheckpoint reads the last acknowledged position. A missing file means nothing has been acknowledged, which is the
// zero position: segments are numbered from 1, so it is before every one of them. A malformed file refuses startup,
// because guessing would either replay already-archived records or skip records the database never saw.
func readCheckpoint(dir string) (position, error) {
	data, err := os.ReadFile(filepath.Join(dir, spillPosName)) //nolint:gosec // dir is the operator's AXIOM_CALL_LOG_DIR
	if errors.Is(err, os.ErrNotExist) {
		return position{}, nil
	}
	if err != nil {
		return position{}, fmt.Errorf("observability: read call log checkpoint: %w", err)
	}
	body, ok := strings.CutPrefix(string(data), spillPosHeader)
	if !ok {
		return position{}, fmt.Errorf("observability: call log checkpoint has no %q header", spillPosHeader)
	}
	fields := strings.Fields(body)
	if len(fields) != 2 {
		return position{}, fmt.Errorf("observability: call log checkpoint %q is not a segment and offset", body)
	}
	seq, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || seq == 0 {
		return position{}, fmt.Errorf("observability: call log checkpoint has an invalid segment %q", fields[0])
	}
	offset, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || offset < int64(len(spillHeader)) {
		return position{}, fmt.Errorf("observability: call log checkpoint has an invalid offset %q", fields[1])
	}
	return position{seq: seq, offset: offset}, nil
}

// spillSeq recognizes one segment's name and returns its number.
func spillSeq(name string) (uint64, bool) {
	if !strings.HasPrefix(name, spillPrefix) || !strings.HasSuffix(name, spillSuffix) {
		return 0, false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(name, spillPrefix), spillSuffix)
	seq, err := strconv.ParseUint(number, 10, 64)
	if err != nil || seq == 0 {
		return 0, false
	}
	return seq, true
}

// scanSegment validates one segment and returns its complete prefix and the number of records at or after from.
// A torn tail is allowed only on the last segment, where it is what a crash during Commit leaves; it is truncated away.
// Any other damage stops startup instead of being repaired: losing a complete record would be worse than refusing to
// start.
func scanSegment(path string, last bool, from int64) (size, after int64, pending bool, err error) {
	file, err := openSegment(path, os.O_RDWR)
	if err != nil {
		return 0, 0, false, err
	}
	defer closeQuietly(file)
	size, valid, after, pending, err := readSegment(file, path, last, from)
	switch {
	case err != nil:
		return 0, 0, false, err
	case size < int64(len(spillHeader)):
		return 0, 0, false, dropHeaderlessSegment(path, size, last)
	case valid != size: // only the last segment may have a torn tail: scanRecords refuses one elsewhere
		if err := truncateSegment(file, valid); err != nil {
			return 0, 0, false, err
		}
	case last:
		// A process crash leaves the last Commits' bytes in the page cache, maybe unsynced; make them durable before
		// the writer stores them and checkpoints past them.
		if err := file.Sync(); err != nil {
			return 0, 0, false, fmt.Errorf("observability: sync call log segment %s: %w", path, err)
		}
	}
	return valid, after, pending, nil
}

// openSegment opens a segment found in the spill directory, for reading and writing (startup) or reading alone (backlog).
func openSegment(path string, flag int) (*os.File, error) {
	file, err := os.OpenFile(path, flag, 0) //nolint:gosec // path is the spill directory plus a validated segment name
	if err != nil {
		return nil, fmt.Errorf("observability: open call log segment %s: %w", path, err)
	}
	return file, nil
}

// readSegment validates one open segment without changing it: its size, where its complete records end, how many of
// them are at or after from, and whether it heads pending records. A segment shorter than its header has no records and
// is the caller's to judge (headerless).
func readSegment(file *os.File, path string, last bool, from int64) (size, valid, after int64, pending bool, err error) {
	info, err := file.Stat()
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("observability: stat call log segment %s: %w", path, err)
	}
	size = info.Size()
	if size < int64(len(spillHeader)) {
		return size, 0, 0, false, nil
	}
	if pending, err = checkSegmentHeader(file, path); err != nil {
		return 0, 0, 0, false, err
	}
	valid, after, err = scanRecords(file, path, last, size, from)
	if err != nil {
		return 0, 0, 0, false, err
	}
	return size, valid, after, pending, nil
}

// headerless judges a segment shorter than its header. Only the last one can be in that state, which a crash leaves
// before the header was durable; an earlier one means data is missing, not just unfinished.
func headerless(path string, size int64, last bool) error {
	switch {
	case last:
		return nil
	case size == 0:
		return fmt.Errorf("observability: call log segment %s is empty", path)
	default:
		return fmt.Errorf("observability: call log segment %s has no %q header", path, spillHeader)
	}
}

// dropHeaderlessSegment removes the last segment when a crash left it before its header was durable (headerless).
func dropHeaderlessSegment(path string, size int64, last bool) error {
	if err := headerless(path, size, last); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("observability: remove headerless call log segment %s: %w", path, err)
	}
	return nil
}

// segmentHeader is the header of a segment headed for pending records, or of one that is not.
func segmentHeader(pending bool) string {
	if pending {
		return spillHeaderPending
	}
	return spillHeader
}

// checkSegmentHeader reads and verifies the fixed header, and reports whether it heads pending records.
func checkSegmentHeader(file *os.File, path string) (pending bool, err error) {
	header := make([]byte, len(spillHeader))
	if _, err := io.ReadFull(file, header); err != nil || string(header) != spillHeader && string(header) != spillHeaderPending {
		return false, fmt.Errorf("observability: call log segment %s has no %q header", path, spillHeader)
	}
	return string(header) == spillHeaderPending, nil
}

// scanRecords validates complete records until the first torn one, which is allowed only at the tail of the last
// segment, and returns where the complete records end and how many are at or after from. from is where the database's
// acknowledged position falls in this segment: it must be the start of a record or the end of the complete ones.
func scanRecords(file *os.File, path string, last bool, size, from int64) (valid, after int64, err error) {
	valid = int64(len(spillHeader))
	boundary := from == valid
	for valid < size {
		n, err := scanRecord(file, path, valid)
		if errors.Is(err, errTornTail) {
			if !last {
				return 0, 0, fmt.Errorf("%w in %s at %d", errTornTail, path, valid)
			}
			break
		}
		if err != nil {
			return 0, 0, err
		}
		boundary = boundary || valid == from
		if valid >= from {
			after++
		}
		valid += recordHeaderBytes + n
	}
	if !boundary && from != valid {
		return 0, 0, fmt.Errorf("observability: call log segment %s: checkpoint at %d is not on a complete record boundary", path, from)
	}
	return valid, after, nil
}

// scanRecord validates one record's length and body. A final record whose body is short is a write that did not finish;
// a complete record whose checksum does not match is damage, and the function says so rather than letting startup
// truncate a record that may already have been served.
func scanRecord(file *os.File, path string, offset int64) (int64, error) {
	var head [recordHeaderBytes]byte
	if _, err := file.ReadAt(head[:], offset); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, errTornTail
		}
		return 0, fmt.Errorf("observability: read call log segment %s at %d: %w", path, offset, err)
	}
	n := int64(binary.BigEndian.Uint32(head[:4]))
	if n <= 0 || n > maxRecordBytes {
		return 0, fmt.Errorf("observability: call log segment %s at %d: record length %d", path, offset, n)
	}
	body := make([]byte, n)
	if _, err := file.ReadAt(body, offset+recordHeaderBytes); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, errTornTail
		}
		return 0, fmt.Errorf("observability: read call log segment %s at %d: %w", path, offset, err)
	}
	if crc32.Checksum(body, crcTable) != binary.BigEndian.Uint32(head[4:]) {
		return 0, fmt.Errorf("observability: call log segment %s at %d: checksum mismatch", path, offset)
	}
	return n, nil
}

// truncateSegment drops a torn tail and makes the truncation visible to the next process before it appends.
func truncateSegment(file *os.File, size int64) error {
	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("observability: truncate call log segment: %w", err)
	}
	if _, err := file.Seek(size, io.SeekStart); err != nil {
		return fmt.Errorf("observability: seek truncated call log segment: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("observability: sync truncated call log segment: %w", err)
	}
	return nil
}

// SpillBacklog is the number of call records in the spill directory dir that the database has not confirmed: the
// records after its checkpoint and the parked pending records. A torn tail is not one, because its Commit never
// returned; damage the next start would refuse is an error. It changes nothing in dir and takes no lock, so it is exact
// only while no process owns dir: the backup holds the directory's lock while it asks, with the node stopped
// (docs/ops/backup-restore.md「备份」).
func SpillBacklog(dir string) (int64, error) {
	checkpoint, err := readCheckpoint(dir)
	if err != nil {
		return 0, err
	}
	files, err := listSegments(dir)
	if err != nil {
		return 0, err
	}
	var n int64
	for i, f := range files {
		if f.seq < checkpoint.seq {
			continue
		}
		from := int64(len(spillHeader))
		if f.seq == checkpoint.seq {
			from = checkpoint.offset
		}
		after, err := segmentBacklog(f.path, i == len(files)-1, from)
		if err != nil {
			return 0, err
		}
		n += after
	}
	parked, err := os.ReadDir(filepath.Join(dir, parkDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("observability: parked call records: %w", err)
	}
	for _, entry := range parked {
		if strings.HasSuffix(entry.Name(), parkSuffix) {
			n++
		}
	}
	return n, nil
}

// segmentBacklog is the number of records at or after from in one segment, which it only reads.
func segmentBacklog(path string, last bool, from int64) (int64, error) {
	file, err := openSegment(path, os.O_RDONLY)
	if err != nil {
		return 0, err
	}
	defer closeQuietly(file)
	size, _, after, _, err := readSegment(file, path, last, from)
	if err == nil && size < int64(len(spillHeader)) {
		err = headerless(path, size, last)
	}
	return after, err
}
