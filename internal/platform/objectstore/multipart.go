package objectstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The multipart protocol is the one S3 defines: initiate, one PUT per part, complete with every part's ETag. It is what
// lets a backup archive pass the 5 GiB single PUT limit; the caller's size decides the part size, so a 5 TiB object still
// fits in 10,000 parts.

type initiateMultipart struct {
	UploadID string `xml:"UploadId"`
}

type multipartPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipart struct {
	XMLName xml.Name        `xml:"CompleteMultipartUpload"`
	Parts   []multipartPart `xml:"Part"`
}

type completedMultipart struct {
	XMLName xml.Name
	ETag    string `xml:"ETag"`
}

// putMultipart sends one object in parts. The body must describe exactly size bytes: the operator command passes a
// file's Stat, and a body that grew or shrank after it fails before anything is uploaded.
func (c *Client) putMultipart(ctx context.Context, up Upload, size int64) (ObjectInfo, error) {
	end, err := up.Body.Seek(0, io.SeekEnd)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: measure %q: %w", up.Key, err)
	}
	if end != size {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q is %d bytes but Size says %d", up.Key, end, size)
	}
	if _, err := up.Body.Seek(0, io.SeekStart); err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: rewind %q: %w", up.Key, err)
	}
	partSize := c.partSize
	if partSize <= 0 {
		partSize = defaultPartSize
	}
	if needed := (size + maxParts - 1) / maxParts; needed > partSize {
		partSize = (needed + (1 << 20) - 1) &^ ((1 << 20) - 1)
	}
	if partSize > maxPartSize {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q needs %d-byte parts, over S3's %d-byte limit: %w",
			up.Key, partSize, int64(maxPartSize), ErrTooLarge)
	}
	contentType := up.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	uploadID, err := c.createMultipart(ctx, up.Key, contentType)
	if err != nil {
		return ObjectInfo{}, err
	}
	parts := make([]multipartPart, 0, (size+partSize-1)/partSize)
	for offset, number := int64(0), 1; offset < size; number++ {
		if err := ctx.Err(); err != nil {
			return ObjectInfo{}, errors.Join(fmt.Errorf("objectstore: put %q: %w", up.Key, err),
				c.abortMultipart(ctx, up.Key, uploadID))
		}
		length := min(partSize, size-offset)
		body := &section{src: up.Body, off: offset, size: length}
		hash, read, err := hashBody(ctx, body, length)
		if err != nil {
			return ObjectInfo{}, errors.Join(fmt.Errorf("objectstore: read %q: %w", up.Key, err),
				c.abortMultipart(ctx, up.Key, uploadID))
		}
		if read != length {
			return ObjectInfo{}, errors.Join(
				fmt.Errorf("objectstore: %q part %d is %d bytes but should be %d", up.Key, number, read, length),
				c.abortMultipart(ctx, up.Key, uploadID))
		}
		etag, err := c.uploadPart(ctx, up.Key, uploadID, number, body, length, hash)
		if err != nil {
			return ObjectInfo{}, errors.Join(err, c.abortMultipart(ctx, up.Key, uploadID))
		}
		parts = append(parts, multipartPart{PartNumber: number, ETag: etag})
		offset += length
	}
	info, err := c.completeMultipart(ctx, up.Key, uploadID, parts, size)
	if err != nil {
		return ObjectInfo{}, errors.Join(err, c.abortMultipart(ctx, up.Key, uploadID))
	}
	return info, nil
}

func (c *Client) createMultipart(ctx context.Context, key, contentType string) (string, error) {
	resp, err := c.do(ctx, request{
		method:      http.MethodPost,
		key:         key,
		query:       url.Values{"uploads": {""}},
		contentType: contentType,
		payloadHash: emptySHA256,
	})
	if err != nil {
		return "", fmt.Errorf("objectstore: start multipart %q: %w", key, err)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("objectstore: start multipart %q: %w", key, remoteError(resp))
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", fmt.Errorf("objectstore: start multipart %q: %w", key, err)
	}
	var result initiateMultipart
	if err := xml.Unmarshal(data, &result); err != nil || result.UploadID == "" {
		return "", fmt.Errorf("objectstore: start multipart %q: the answer names no upload", key)
	}
	return result.UploadID, nil
}

func (c *Client) uploadPart(ctx context.Context, key, uploadID string, number int, body io.ReadSeeker, size int64, hash string) (string, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodPut,
		key:    key,
		query: url.Values{
			"partNumber": {strconv.Itoa(number)},
			"uploadId":   {uploadID},
		},
		body:        body,
		size:        size,
		payloadHash: hash,
	})
	if err != nil {
		return "", fmt.Errorf("objectstore: put %q part %d: %w", key, number, err)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("objectstore: put %q part %d: %w", key, number, remoteError(resp))
	}
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	_ = resp.Body.Close() //nolint:errcheck // a part has no useful response body
	if etag == "" {
		return "", fmt.Errorf("objectstore: put %q part %d: the answer carries no ETag", key, number)
	}
	return etag, nil
}

func (c *Client) completeMultipart(ctx context.Context, key, uploadID string, parts []multipartPart, size int64) (ObjectInfo, error) {
	payload, err := xml.Marshal(completeMultipart{Parts: parts})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: complete %q: %w", key, err)
	}
	for attempt := 1; ; attempt++ {
		resp, err := c.do(ctx, request{
			method:      http.MethodPost,
			key:         key,
			query:       url.Values{"uploadId": {uploadID}},
			body:        bytes.NewReader(payload),
			size:        int64(len(payload)),
			contentType: "application/xml",
			payloadHash: sumHex(payload),
		})
		if err != nil {
			return ObjectInfo{}, fmt.Errorf("objectstore: complete %q: %w", key, err)
		}
		if resp.StatusCode/100 != 2 {
			return ObjectInfo{}, fmt.Errorf("objectstore: complete %q: %w", key, remoteError(resp))
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxListBody))
		closeErr := resp.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return ObjectInfo{}, fmt.Errorf("objectstore: complete %q: %w", key, err)
		}
		var result completedMultipart
		parseErr := xml.Unmarshal(data, &result)
		if parseErr == nil && result.XMLName.Local == "CompleteMultipartUploadResult" && result.ETag != "" {
			return ObjectInfo{Key: key, Size: size, ETag: result.ETag}, nil
		}
		// S3 may answer 200 with an Error document instead of a result; the status alone would call that a success.
		var remote struct {
			Code      string `xml:"Code"`
			Message   string `xml:"Message"`
			RequestID string `xml:"RequestId"`
		}
		if xml.Unmarshal(data, &remote) == nil && remote.Code != "" {
			remoteErr := &Error{Status: resp.StatusCode, Code: remote.Code, Message: remote.Message, RequestID: remote.RequestID}
			if attempt < c.attempts && retryableCompleteCode(remote.Code) {
				if err := pause(ctx, c.backoff(attempt, 0)); err != nil {
					return ObjectInfo{}, errors.Join(remoteErr, err)
				}
				continue
			}
			return ObjectInfo{}, fmt.Errorf("objectstore: complete %q: %w", key, remoteErr)
		}
		if parseErr == nil {
			parseErr = errors.New("the answer is not a completion result with an ETag")
		}
		return ObjectInfo{}, fmt.Errorf("objectstore: complete %q: %w", key, parseErr)
	}
}

// retryableCompleteCode is the subset of S3 error codes that a CompleteMultipartUpload may answer with 200 and that can
// pass on a later attempt. Anything else (InvalidPart, NoSuchUpload) is final and the upload is aborted.
func retryableCompleteCode(code string) bool {
	switch code {
	case "InternalError", "ServiceUnavailable", "SlowDown", "RequestTimeout", "RequestTimeoutException", "OperationAborted":
		return true
	default:
		return false
	}
}

// abortMultipart drops the parts a failed upload left. It runs on its own short deadline: the caller's context is
// usually the failure, so inheriting it would guarantee the cleanup never happens.
func (c *Client) abortMultipart(ctx context.Context, key, uploadID string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	resp, err := c.do(cleanup, request{
		method:      http.MethodDelete,
		key:         key,
		query:       url.Values{"uploadId": {uploadID}},
		payloadHash: emptySHA256,
	})
	if err != nil {
		return fmt.Errorf("objectstore: abort multipart %q: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		_ = resp.Body.Close() //nolint:errcheck // the upload is gone either way
		return nil
	}
	return fmt.Errorf("objectstore: abort multipart %q: %w", key, remoteError(resp))
}

// section is one part's window on the caller's seekable body. Read seeks the source each time, so a request that rewinds
// the part to its start cannot drag the next part back to the beginning of the object.
type section struct {
	src  io.ReadSeeker
	off  int64
	size int64
	pos  int64
}

func (s *section) Read(p []byte) (int, error) {
	if s.pos >= s.size {
		return 0, io.EOF
	}
	if int64(len(p)) > s.size-s.pos {
		p = p[:s.size-s.pos]
	}
	if _, err := s.src.Seek(s.off+s.pos, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := s.src.Read(p)
	s.pos += int64(n)
	return n, err
}

func (s *section) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = s.pos + offset
	case io.SeekEnd:
		next = s.size + offset
	default:
		return 0, errors.New("objectstore: invalid section whence")
	}
	if next < 0 {
		return 0, errors.New("objectstore: negative section offset")
	}
	s.pos = next
	return next, nil
}
