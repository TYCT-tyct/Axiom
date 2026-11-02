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
// duplicate a row.
func openSpill(dir string, capacity int) (*spill, error) {
	if dir == "" {
		return nil, errors.New("observability: the call log needs a spill directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
	checkpoint, haveCheckpoint, err := readCheckpoint(dir)
	if err != nil {
		return fail(err)
	}
	files, err := listSegments(dir)
	if err != nil {
		return fail(err)
	}
	s := &spill{
		dir: dir, lock: lock, maxBytes: int64(capacity)*(maxRecordBytes+recordHeaderBytes) + spillSegmentBytes,
		nextSeq: 1, wake: make(chan struct{}, 1), stop: make(chan struct{}),
	}
	if err := s.loadSegments(files, checkpoint, haveCheckpoint); err != nil {
		return fail(err)
	}
	if err := s.openActive(checkpoint, haveCheckpoint); err != nil {
		return fail(err)
	}
	if len(s.segments) > 0 {
		s.read = s.segments[0]
		s.readOffset = int64(len(spillHeader))
		if haveCheckpoint && s.read.seq == checkpoint.seq {
			s.readOffset = checkpoint.offset
		}
		if s.readOffset > s.read.size {
			return fail(fmt.Errorf("observability: call log checkpoint %d/%d is past the end of %s",
				checkpoint.seq, checkpoint.offset, s.read.path))
		}
	}
	return s, nil
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

// loadSegments validates the files and records how many records the writer must replay. A segment entirely before the
// acknowledged position is dropped without being read: its records are already in the database, and replaying them
// after the month's partition has been archived would insert them into the default partition as duplicates.
func (s *spill) loadSegments(files []segmentFile, checkpoint position, haveCheckpoint bool) error {
	for i, f := range files {
		if i > 0 && f.seq == files[i-1].seq {
			return fmt.Errorf("observability: two call log segments numbered %d", f.seq)
		}
		if haveCheckpoint && f.seq < checkpoint.seq {
			removeQuietly(f.path)
			continue
		}
		from := int64(len(spillHeader))
		if haveCheckpoint && f.seq == checkpoint.seq {
			from = checkpoint.offset
		}
		size, records, after, err := scanSegment(f.path, i == len(files)-1, from)
		if err != nil {
			return err
		}
		if size == 0 {
			continue // scanSegment removed an empty or torn-header last segment
		}
		if haveCheckpoint && f.seq == checkpoint.seq && from > size {
			return fmt.Errorf("observability: call log checkpoint %d/%d is past the end of %s", checkpoint.seq, from, f.path)
		}
		s.segments = append(s.segments, &spillSegment{seq: f.seq, path: f.path, size: size})
		s.totalBytes += size
		s.pending += after
		if records < after {
			return fmt.Errorf("observability: call log segment %s: %d records after the checkpoint of %d", f.path, after, records)
		}
	}
	return nil
}

// openActive opens the last segment for appends, or starts a fresh one when the last is full or fully acknowledged. A
// fully acknowledged segment is rotated away without waiting for it to fill: otherwise a quiet node would keep months
// of already-stored records in its active segment and replay them after a restart.
func (s *spill) openActive(checkpoint position, haveCheckpoint bool) error {
	next := uint64(1)
	if haveCheckpoint && checkpoint.seq >= next {
		next = checkpoint.seq + 1
	}
	if len(s.segments) == 0 {
		s.nextSeq = next
		return s.createSegmentLocked(next)
	}
	last := s.segments[len(s.segments)-1]
	if last.seq >= next {
		next = last.seq + 1
	}
	ackedToEnd := haveCheckpoint && last.seq == checkpoint.seq && checkpoint.offset >= last.size
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
		return s.createSegmentLocked(next)
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

// readCheckpoint reads the last acknowledged position. A missing file means nothing has been acknowledged; a malformed
// one refuses startup, because guessing would either replay already-archived records or skip records the database
// never saw.
func readCheckpoint(dir string) (position, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, spillPosName))
	if errors.Is(err, os.ErrNotExist) {
		return position{}, false, nil
	}
	if err != nil {
		return position{}, false, fmt.Errorf("observability: read call log checkpoint: %w", err)
	}
	body, ok := strings.CutPrefix(string(data), spillPosHeader)
	if !ok {
		return position{}, false, fmt.Errorf("observability: call log checkpoint has no %q header", spillPosHeader)
	}
	fields := strings.Fields(body)
	if len(fields) != 2 {
		return position{}, false, fmt.Errorf("observability: call log checkpoint %q is not a segment and offset", body)
	}
	seq, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || seq == 0 {
		return position{}, false, fmt.Errorf("observability: call log checkpoint has an invalid segment %q", fields[0])
	}
	offset, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || offset < int64(len(spillHeader)) {
		return position{}, false, fmt.Errorf("observability: call log checkpoint has an invalid offset %q", fields[1])
	}
	return position{seq: seq, offset: offset}, true, nil
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

// scanSegment validates one segment and returns its complete prefix, its record count and the records at or after from.
// A torn tail is allowed only on the last segment, where it is what a crash during Commit leaves; it is truncated away.
// Any other damage stops startup instead of being repaired: losing a complete record would be worse than refusing to
// start.
func scanSegment(path string, last bool, from int64) (size, records, after int64, err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // path is the configured spill directory plus a validated segment name
	if err != nil {
		return 0, 0, 0, fmt.Errorf("observability: open call log segment %s: %w", path, err)
	}
	defer closeQuietly(file)
	info, err := file.Stat()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("observability: stat call log segment %s: %w", path, err)
	}
	size = info.Size()
	if size < int64(len(spillHeader)) {
		size, records, err = dropHeaderlessSegment(path, size, last)
		return size, records, 0, err
	}
	if err := checkSegmentHeader(file, path); err != nil {
		return 0, 0, 0, err
	}
	valid, records, after, err := scanRecords(file, path, last, size, from)
	if err != nil {
		return 0, 0, 0, err
	}
	if valid != size {
		if !last {
			return 0, 0, 0, fmt.Errorf("%w in %s at %d", errTornTail, path, valid)
		}
		if err := truncateSegment(file, valid); err != nil {
			return 0, 0, 0, err
		}
	} else if last {
		// A process crash leaves the last Commits' bytes in the page cache, maybe unsynced; make them durable before
		// the writer stores them and checkpoints past them.
		if err := file.Sync(); err != nil {
			return 0, 0, 0, fmt.Errorf("observability: sync call log segment %s: %w", path, err)
		}
	}
	return valid, records, after, nil
}

// dropHeaderlessSegment removes a segment a crash left before its header was durable. Only the last one can be in that
// state; an earlier one means data is missing, not just unfinished.
func dropHeaderlessSegment(path string, size int64, last bool) (int64, int64, error) {
	if !last {
		if size == 0 {
			return 0, 0, fmt.Errorf("observability: call log segment %s is empty", path)
		}
		return 0, 0, fmt.Errorf("observability: call log segment %s has no %q header", path, spillHeader)
	}
	if err := os.Remove(path); err != nil {
		return 0, 0, fmt.Errorf("observability: remove headerless call log segment %s: %w", path, err)
	}
	return 0, 0, nil
}

// checkSegmentHeader reads and verifies the fixed header.
func checkSegmentHeader(file *os.File, path string) error {
	header := make([]byte, len(spillHeader))
	if _, err := io.ReadFull(file, header); err != nil || string(header) != spillHeader {
		return fmt.Errorf("observability: call log segment %s has no %q header", path, spillHeader)
	}
	return nil
}

// scanRecords validates complete records until the first torn one, which is allowed only at the tail. after counts the
// records at or after from, the position the database acknowledged before this start.
func scanRecords(file *os.File, path string, last bool, size, from int64) (valid, records, after int64, err error) {
	valid = int64(len(spillHeader))
	boundary := from == valid || from == size
	for valid < size {
		n, err := scanRecord(file, path, valid)
		if err != nil {
			if errors.Is(err, errTornTail) {
				if from > valid || !boundary {
					return 0, 0, 0, fmt.Errorf("observability: call log segment %s: checkpoint at %d is not on a complete record boundary", path, from)
				}
				if !last {
					return 0, 0, 0, fmt.Errorf("%w in %s at %d", errTornTail, path, valid)
				}
				return valid, records, after, nil
			}
			return 0, 0, 0, err
		}
		if valid == from {
			boundary = true
		}
		if valid >= from {
			after++
		}
		valid += recordHeaderBytes + n
		records++
	}
	if from != int64(len(spillHeader)) && from != size && !boundary {
		return 0, 0, 0, fmt.Errorf("observability: call log segment %s: checkpoint at %d is not on a record boundary", path, from)
	}
	return valid, records, after, nil
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
