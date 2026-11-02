package observability

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"

	"axiom/internal/platform/jsonx"
)

// availableLocked advances past finished segments and reports whether a record is ready to read.
func (s *spill) availableLocked() bool {
	for {
		if s.read == nil {
			if len(s.segments) == 0 {
				return false
			}
			s.read = s.segments[0]
			s.readOffset = int64(len(spillHeader))
		}
		if s.readOffset < s.readableLocked(s.read) {
			return true
		}
		if s.read == s.active {
			return false // the writer has read everything appended so far; a new append wakes it
		}
		if !s.advanceReadLocked() {
			return false
		}
	}
}

// readLocked reads up to max complete records from the current segment, then the next, and returns the position of the
// last one. It stops at the durable end of the active segment: more records may still be appended or synced.
func (s *spill) readLocked(max int) ([]queued, position, error) {
	var out []queued
	var end position
	for len(out) < max {
		if s.read == nil {
			if len(s.segments) == 0 {
				break
			}
			s.read = s.segments[0]
			s.readOffset = int64(len(spillHeader))
		}
		if s.readOffset >= s.readableLocked(s.read) {
			if s.read == s.active || !s.advanceReadLocked() {
				break
			}
			continue
		}
		record, n, err := s.readOneLocked(s.read, s.readOffset)
		if err != nil {
			s.broken = err
			return out, end, err
		}
		s.readOffset += n
		out = append(out, record)
		end = position{seq: s.read.seq, offset: s.readOffset}
	}
	return out, end, nil
}

// advanceReadLocked moves to the next segment and reports whether there was one. The old segment stays in segments
// until its batch is acknowledged, so the position in end can still name it. With no next segment the cursor stays at
// the active segment's end, where a later append grows the readable range.
func (s *spill) advanceReadLocked() bool {
	if s.readFile != nil {
		closeQuietly(s.readFile)
		s.readFile = nil
	}
	for i, segment := range s.segments {
		if segment != s.read {
			continue
		}
		if i+1 < len(s.segments) {
			s.read = s.segments[i+1]
			s.readOffset = int64(len(spillHeader))
			return true
		}
		s.readOffset = segment.size
		return false
	}
	s.read = nil
	s.readOffset = 0
	return len(s.segments) > 0
}

// readOneLocked reads and decodes one record at offset.
func (s *spill) readOneLocked(segment *spillSegment, offset int64) (queued, int64, error) {
	if s.readFile == nil {
		file, err := os.Open(segment.path)
		if err != nil {
			return queued{}, 0, fmt.Errorf("observability: open call log segment: %w", err)
		}
		s.readFile = file
	}
	var head [recordHeaderBytes]byte
	if _, err := s.readFile.ReadAt(head[:], offset); err != nil {
		return queued{}, 0, fmt.Errorf("observability: read call log record at %d: %w", offset, err)
	}
	n := int64(binary.BigEndian.Uint32(head[:4]))
	sum := binary.BigEndian.Uint32(head[4:])
	if n <= 0 || n > maxRecordBytes {
		return queued{}, 0, fmt.Errorf("observability: call log record at %d has length %d", offset, n)
	}
	body := make([]byte, n)
	if _, err := s.readFile.ReadAt(body, offset+recordHeaderBytes); err != nil {
		return queued{}, 0, fmt.Errorf("observability: read call log record at %d: %w", offset, err)
	}
	if crc32.Checksum(body, crcTable) != sum {
		return queued{}, 0, fmt.Errorf("observability: call log record at %d: checksum mismatch", offset)
	}
	var record walRecord
	if err := jsonx.Unmarshal(body, &record); err != nil {
		return queued{}, 0, fmt.Errorf("observability: decode call log record at %d: %w", offset, err)
	}
	return record.queued(), recordHeaderBytes + n, nil
}
