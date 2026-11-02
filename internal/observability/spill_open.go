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

// errTornTail is a record or header whose last write did not finish. It is allowed only on the last segment, where the
// next open truncates it; anywhere else it means a complete record may be missing and startup fails.
var errTornTail = errors.New("observability: torn call log tail")

// segmentFile is a segment found in the spill directory, before it is opened.
type segmentFile struct {
	seq  uint64
	path string
}

// openSpill opens or creates dir and validates every segment it finds, dropping a torn tail from the last one. The
// writer replays every record in every segment, including records whose database write committed before the process
// died; the inserts are idempotent, so replaying them cannot duplicate a row.
func openSpill(dir string, capacity int) (*spill, error) {
	if dir == "" {
		return nil, errors.New("observability: the call log needs a spill directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("observability: call log spill directory: %w", err)
	}
	files, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	s := &spill{
		dir: dir, maxBytes: int64(capacity)*(maxRecordBytes+recordHeaderBytes) + spillSegmentBytes,
		nextSeq: 1, wake: make(chan struct{}, 1), stop: make(chan struct{}),
	}
	if err := s.loadSegments(files); err != nil {
		return nil, err
	}
	if err := s.openActive(); err != nil {
		return nil, err
	}
	if len(s.segments) > 0 {
		s.read = s.segments[0]
		s.readOffset = int64(len(spillHeader))
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

// loadSegments validates the files and records how many records the writer must replay.
func (s *spill) loadSegments(files []segmentFile) error {
	for i, f := range files {
		if i > 0 && f.seq == files[i-1].seq {
			return fmt.Errorf("observability: two call log segments numbered %d", f.seq)
		}
		size, records, err := scanSegment(f.path, i == len(files)-1)
		if err != nil {
			return err
		}
		if size == 0 {
			continue // scanSegment removed an empty or torn-header last segment
		}
		s.segments = append(s.segments, &spillSegment{seq: f.seq, path: f.path, size: size})
		s.totalBytes += size
		s.pending += records
	}
	return nil
}

// openActive opens the last segment for appends, or starts a fresh one when the last is full.
func (s *spill) openActive() error {
	if len(s.segments) == 0 {
		return s.createSegmentLocked(1)
	}
	last := s.segments[len(s.segments)-1]
	s.nextSeq = last.seq + 1
	if last.size >= spillSegmentBytes {
		return s.createSegmentLocked(s.nextSeq)
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
	return nil
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

// scanSegment validates one segment and returns its complete prefix and record count. A torn tail is allowed only on
// the last segment, where it is what a crash during Enqueue leaves; it is truncated away. Any other damage stops
// startup instead of being repaired: losing a complete record would be worse than refusing to start.
func scanSegment(path string, last bool) (size, records int64, err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // path is the configured spill directory plus a validated segment name
	if err != nil {
		return 0, 0, fmt.Errorf("observability: open call log segment %s: %w", path, err)
	}
	defer closeQuietly(file)
	info, err := file.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("observability: stat call log segment %s: %w", path, err)
	}
	size = info.Size()
	if size < int64(len(spillHeader)) {
		return dropHeaderlessSegment(path, size, last)
	}
	if err := checkSegmentHeader(file, path); err != nil {
		return 0, 0, err
	}
	valid, records, err := scanRecords(file, path, last, size)
	if err != nil {
		return 0, 0, err
	}
	if valid != size {
		if !last {
			return 0, 0, fmt.Errorf("%w in %s at %d", errTornTail, path, valid)
		}
		if err := truncateSegment(file, valid); err != nil {
			return 0, 0, err
		}
	}
	return valid, records, nil
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

// scanRecords validates complete records until the first torn one, which is allowed only at the tail.
func scanRecords(file *os.File, path string, last bool, size int64) (valid, records int64, err error) {
	valid = int64(len(spillHeader))
	for valid < size {
		n, err := scanRecord(file, path, valid, size)
		if err != nil {
			if last && errors.Is(err, errTornTail) {
				return valid, records, nil
			}
			if errors.Is(err, errTornTail) {
				return 0, 0, fmt.Errorf("%w in %s at %d", errTornTail, path, valid)
			}
			return 0, 0, err
		}
		valid += recordHeaderBytes + n
		records++
	}
	return valid, records, nil
}

// scanRecord validates one record's length, body and checksum. A final record whose checksum does not match is the
// same as a torn one: the writer never returned the append that would have completed it.
func scanRecord(file *os.File, path string, offset, size int64) (int64, error) {
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
		if offset+recordHeaderBytes+n == size {
			return 0, errTornTail
		}
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
