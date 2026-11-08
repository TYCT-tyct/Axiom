// Package objectstore reads and writes objects through the S3 API. It is the platform's only client for object
// storage: backups use it with their own bucket and credentials in the ops job, and archive and content features take
// this client when they land. Conversation bodies are not written through it: the product switch that would save them
// is still off (docs/domains/observability.md「保留与开关」). The controlled export reads them through Get, as
// ciphertext only.
//
// Every operation requires a context deadline, retries only failures that may pass, and never follows a redirect.
// The rules and the choices behind them are in this directory's README.
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

	"axiom/internal/platform/secret"
)

var (
	// ErrNotConfigured says the deployment has no object store. Features that need one stay off rather than fail a
	// call or write a partial archive.
	ErrNotConfigured = errors.New("objectstore: no bucket is configured")
	// ErrDeadlineRequired prevents an operation from waiting on a bucket forever.
	ErrDeadlineRequired = errors.New("objectstore: request context requires a deadline")
	// ErrTooLarge says the body is over the single PUT limit, so it cannot be one request.
	ErrTooLarge = errors.New("objectstore: object is over the single PUT limit")
	// ErrNotFound marks a missing object or bucket, and can be tested with errors.Is on any *Error with status 404.
	ErrNotFound = errors.New("objectstore: not found")
	// ErrExists says a NoOverwrite upload found its key taken (412), and can be tested with errors.Is.
	ErrExists = errors.New("objectstore: the key exists")
)

// requireDeadline is the rule every operation follows before it waits on anything: the caller must bound the whole
// call. A canceled call stops before its next read or attempt.
func requireDeadline(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return ErrDeadlineRequired
	}
	return ctx.Err()
}

// ObjectInfo is what a HEAD or a listing says about one object, without its bytes.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
}

// Object is an object opened for reading. The caller closes Body.
type Object struct {
	ObjectInfo
	Body io.ReadCloser
}

// Upload is one Put: the object's key and bytes. Size is the byte count when the caller knows it, which a file's
// Stat normally does; -1 counts the body while hashing it. ContentType defaults to application/octet-stream. The body
// is read from its start; a Read that blocks must be unblocked by the body's owner, because ctx cannot interrupt it.
// NoOverwrite sends If-None-Match: *, so the bucket refuses a key that exists with ErrExists instead of replacing it;
// such an upload is always one PUT, because a multipart upload checks the condition only when it completes.
type Upload struct {
	Key         string
	Body        io.ReadSeeker
	Size        int64
	ContentType string
	NoOverwrite bool
}

// multipart reports whether up goes in parts: a known size over threshold, and no condition to keep.
func (up Upload) multipart(threshold int64) bool { return up.Size > threshold && !up.NoOverwrite }

// ListOptions filters one page. Prefix may be empty for the whole bucket; MaxKeys defaults to 1000, S3's maximum.
type ListOptions struct {
	Prefix            string
	ContinuationToken string
	MaxKeys           int
}

// ListPage is one page and the token for the next; NextToken is empty when the listing ended.
type ListPage struct {
	Objects   []ObjectInfo
	NextToken string
}

// Client is one bucket, reused across operations and safe for concurrent use.
type Client struct {
	endpoint    *url.URL
	bucket      string
	region      string
	accessKeyID string
	secretKey   secret.Secret
	pathStyle   bool
	transport   *http.Transport
	now         func() time.Time
	attempts    int
	retryBase   time.Duration
	retryMax    time.Duration
	// partSize and threshold are fields so the tests can exercise several parts without moving sixteen mebibytes;
	// production keeps the constants.
	partSize  int64
	threshold int64
}

// Open checks the configuration and builds the client. It does not contact the bucket: a deployment may start while
// the store is briefly unreachable, and Check is how an operator verifies it.
func Open(cfg Config) (*Client, error) {
	if !cfg.Configured() {
		return nil, ErrNotConfigured
	}
	endpoint, err := cfg.check()
	if err != nil {
		return nil, err
	}
	if !cfg.PathStyle {
		// Virtual-host addressing carries the bucket in the host name; the port, if any, stays after it.
		endpoint.Host = cfg.Bucket + "." + endpoint.Host
	}
	return &Client{
		endpoint:    endpoint,
		bucket:      cfg.Bucket,
		region:      cfg.Region,
		accessKeyID: cfg.AccessKeyID,
		secretKey:   cfg.SecretAccessKey,
		pathStyle:   cfg.PathStyle,
		transport:   newTransport(),
		now:         time.Now,
		attempts:    maxAttempts,
		retryBase:   retryBase,
		retryMax:    retryMax,
		partSize:    defaultPartSize,
		threshold:   multipartThreshold,
	}, nil
}

// Close releases idle connections without interrupting an operation in flight.
func (c *Client) Close() { c.transport.CloseIdleConnections() }

// Check verifies that the bucket exists and that the credentials may address it. It is a HEAD on the bucket, so it
// reads nothing and writes nothing.
func (c *Client) Check(ctx context.Context) error {
	resp, err := c.do(ctx, request{method: http.MethodHead, payloadHash: emptySHA256})
	if err != nil {
		return fmt.Errorf("objectstore: check bucket %q: %w", c.bucket, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("objectstore: check bucket %q: %w", c.bucket, remoteError(resp))
	}
	_ = resp.Body.Close() //nolint:errcheck // a HEAD has no body; closing its empty reader reports nothing useful
	return nil
}

// Create makes the bucket when it is absent, reporting whether this call created it. An existing bucket that these
// credentials can address is used without a second PUT only when allowExisting is set, because S3 resets an existing
// bucket's ACL on CreateBucket and a HEAD cannot prove ownership: a public ListBucket policy on another owner's bucket
// also answers 200. The PUT is only sent after a HEAD says 404. A conflict from that PUT must name
// BucketAlreadyOwnedByYou; BucketAlreadyExists means another account owns the name.
func (c *Client) Create(ctx context.Context, allowExisting bool) (bool, error) {
	if err := c.Check(ctx); err == nil {
		if !allowExisting {
			return false, c.bucketExists()
		}
		return false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return false, fmt.Errorf("objectstore: create bucket %q: %w", c.bucket, err)
	}
	put, err := c.createBucketPut(ctx)
	if err != nil {
		return false, err
	}
	resp, err := c.do(ctx, put)
	if err != nil {
		return false, fmt.Errorf("objectstore: create bucket %q: %w", c.bucket, err)
	}
	if resp.StatusCode/100 == 2 {
		_ = resp.Body.Close() //nolint:errcheck // no body carries a result here
		return true, nil
	}
	if resp.StatusCode == http.StatusConflict {
		return c.bucketConflict(resp, allowExisting)
	}
	return false, fmt.Errorf("objectstore: create bucket %q: %w", c.bucket, remoteError(resp))
}

// createBucketPut is the CreateBucket request: us-east-1 sends no body, every other region names itself in one.
func (c *Client) createBucketPut(ctx context.Context) (request, error) {
	body, contentType, err := createBucketBody(c.region)
	if err != nil {
		return request{}, fmt.Errorf("objectstore: create bucket %q: %w", c.bucket, err)
	}
	put := request{method: http.MethodPut, contentType: contentType, payloadHash: emptySHA256}
	if len(body) == 0 {
		return put, nil
	}
	reader := bytes.NewReader(body)
	hash, size, err := hashBody(ctx, reader, int64(len(body)))
	if err != nil {
		return request{}, fmt.Errorf("objectstore: create bucket %q: %w", c.bucket, err)
	}
	put.body, put.size, put.payloadHash = reader, size, hash
	return put, nil
}

// bucketConflict answers a 409 CreateBucket: S3 reports a bucket this account owns as BucketAlreadyOwnedByYou and
// another account's as BucketAlreadyExists. The code decides, because a public list policy on another owner's bucket
// also answers HEAD 200.
func (c *Client) bucketConflict(resp *http.Response, allowExisting bool) (bool, error) {
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	_ = resp.Body.Close() //nolint:errcheck // the body is already read; a close failure adds nothing
	remote := errorDocument(resp.StatusCode, data)
	if remote != nil && remote.Code == "BucketAlreadyOwnedByYou" {
		if !allowExisting {
			return false, c.bucketExists()
		}
		return false, nil
	}
	if remote == nil {
		remote = &Error{Status: resp.StatusCode}
	}
	remote.cause = readErr
	return false, fmt.Errorf("objectstore: create bucket %q: %w", c.bucket, remote)
}

func (c *Client) bucketExists() error {
	return fmt.Errorf("objectstore: create bucket %q: the bucket already exists; pass allowExisting only if you own it", c.bucket)
}

// createBucketBody is empty for us-east-1, where S3 rejects a LocationConstraint naming it; other regions need one.
func createBucketBody(region string) ([]byte, string, error) {
	if region == "" || region == "us-east-1" {
		return nil, "", nil
	}
	body, err := xml.Marshal(struct {
		XMLName  xml.Name `xml:"CreateBucketConfiguration"`
		Xmlns    string   `xml:"xmlns,attr"`
		Location string   `xml:"LocationConstraint"`
	}{Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Location: region})
	if err != nil {
		return nil, "", err
	}
	return body, "application/xml", nil
}

// Put writes one object. A failed attempt re-sends the body from its start, so the body must be seekable; the same
// key is overwritten, as S3 defines PUT, unless the upload says NoOverwrite. A known size over the multipart threshold
// goes up in parts, so an archive is not bounded by the single PUT limit; an unknown size is counted while hashing and
// stays a single PUT.
func (c *Client) Put(ctx context.Context, up Upload) (ObjectInfo, error) {
	if err := checkKey(up.Key); err != nil {
		return ObjectInfo{}, err
	}
	if err := requireDeadline(ctx); err != nil {
		return ObjectInfo{}, err
	}
	if up.Size > maxObjectSize {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q is over the %d-byte object limit: %w", up.Key, int64(maxObjectSize), ErrTooLarge)
	}
	if up.multipart(c.threshold) {
		return c.putMultipart(ctx, up, up.Size)
	}
	hash, size, err := hashBody(ctx, up.Body, maxSinglePut)
	if errors.Is(err, ErrTooLarge) {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q is over the %d-byte single PUT limit: %w", up.Key, int64(maxSinglePut), ErrTooLarge)
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: read %q: %w", up.Key, err)
	}
	if up.Size >= 0 && up.Size != size {
		return ObjectInfo{}, fmt.Errorf("objectstore: %q is %d bytes but Size says %d", up.Key, size, up.Size)
	}
	contentType := up.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	resp, err := c.do(ctx, request{
		method:      http.MethodPut,
		key:         up.Key,
		body:        up.Body,
		size:        size,
		contentType: contentType,
		payloadHash: hash,
		ifNoneMatch: up.NoOverwrite,
	})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: put %q: %w", up.Key, err)
	}
	if resp.StatusCode/100 != 2 {
		return ObjectInfo{}, fmt.Errorf("objectstore: put %q: %w", up.Key, remoteError(resp))
	}
	info := infoFrom(resp, up.Key)
	info.Size = size
	_ = resp.Body.Close() //nolint:errcheck // a PUT response body is empty; the status carried the answer
	return info, nil
}

// Get opens one object for reading. The caller closes Body, and a read error after the headers is reported to the
// caller rather than retried: the bytes already handed out cannot be taken back.
func (c *Client) Get(ctx context.Context, key string) (*Object, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{method: http.MethodGet, key: key, payloadHash: emptySHA256})
	if err != nil {
		return nil, fmt.Errorf("objectstore: get %q: %w", key, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("objectstore: get %q: %w", key, remoteError(resp))
	}
	return &Object{ObjectInfo: infoFrom(resp, key), Body: resp.Body}, nil
}

// Head reports one object's metadata without reading it.
func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if err := checkKey(key); err != nil {
		return ObjectInfo{}, err
	}
	resp, err := c.do(ctx, request{method: http.MethodHead, key: key, payloadHash: emptySHA256})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: head %q: %w", key, err)
	}
	if resp.StatusCode/100 != 2 {
		return ObjectInfo{}, fmt.Errorf("objectstore: head %q: %w", key, remoteError(resp))
	}
	_ = resp.Body.Close() //nolint:errcheck // a HEAD has no body
	return infoFrom(resp, key), nil
}

// Delete removes one object. A key that is already absent is success: S3's DELETE is idempotent, and so is an archive
// cleanup that runs twice.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	resp, err := c.do(ctx, request{method: http.MethodDelete, key: key, payloadHash: emptySHA256})
	if err != nil {
		return fmt.Errorf("objectstore: delete %q: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close() //nolint:errcheck // the object is gone either way
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("objectstore: delete %q: %w", key, remoteError(resp))
	}
	_ = resp.Body.Close() //nolint:errcheck // a DELETE response body is empty
	return nil
}

// List returns one page of objects. Callers that need the whole prefix follow NextToken until it is empty.
func (c *Client) List(ctx context.Context, opts ListOptions) (ListPage, error) {
	query, err := listQuery(opts)
	if err != nil {
		return ListPage{}, err
	}
	resp, err := c.do(ctx, request{method: http.MethodGet, query: query, payloadHash: emptySHA256})
	if err != nil {
		return ListPage{}, fmt.Errorf("objectstore: list: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return ListPage{}, fmt.Errorf("objectstore: list: %w", remoteError(resp))
	}
	defer resp.Body.Close() //nolint:errcheck // the decoded page is the result; a close after a full read reports nothing
	var result struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			ETag         string `xml:"ETag"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	decoder := xml.NewDecoder(io.LimitReader(resp.Body, maxListBody))
	if err := decoder.Decode(&result); err != nil {
		return ListPage{}, fmt.Errorf("objectstore: read listing: %w", err)
	}
	page := ListPage{NextToken: result.NextContinuationToken}
	if !result.IsTruncated {
		page.NextToken = ""
	}
	for _, item := range result.Contents {
		var modified time.Time
		if parsed, err := time.Parse(time.RFC3339, item.LastModified); err == nil {
			modified = parsed
		}
		page.Objects = append(page.Objects, ObjectInfo{
			Key:          item.Key,
			Size:         item.Size,
			ETag:         item.ETag,
			LastModified: modified,
		})
	}
	return page, nil
}

// listQuery turns the page options into the S3 list-type=2 query, checking the limits S3 defines.
func listQuery(opts ListOptions) (url.Values, error) {
	if len(opts.Prefix) > 1024 {
		return nil, errors.New("objectstore: prefix is longer than 1024 bytes")
	}
	maxKeys := opts.MaxKeys
	if maxKeys == 0 {
		maxKeys = 1000
	}
	if maxKeys < 1 || maxKeys > 1000 {
		return nil, fmt.Errorf("objectstore: MaxKeys wants 1 to 1000, got %d", maxKeys)
	}
	query := url.Values{
		"list-type": {"2"},
		"max-keys":  {strconv.Itoa(maxKeys)},
	}
	if opts.Prefix != "" {
		query.Set("prefix", opts.Prefix)
	}
	if opts.ContinuationToken != "" {
		query.Set("continuation-token", opts.ContinuationToken)
	}
	return query, nil
}

// infoFrom reads the metadata headers S3 puts on GET, HEAD and PUT responses.
func infoFrom(resp *http.Response, key string) ObjectInfo {
	info := ObjectInfo{
		Key:         key,
		ETag:        resp.Header.Get("ETag"),
		ContentType: resp.Header.Get("Content-Type"),
	}
	if length := resp.Header.Get("Content-Length"); length != "" {
		if parsed, err := strconv.ParseInt(length, 10, 64); err == nil {
			info.Size = parsed
		}
	}
	if modified := resp.Header.Get("Last-Modified"); modified != "" {
		if parsed, err := http.ParseTime(modified); err == nil {
			info.LastModified = parsed
		}
	}
	return info
}

// checkKey refuses a key that cannot address one object, not the naming rules of a vendor: those differ.
func checkKey(key string) error {
	switch {
	case key == "":
		return errors.New("objectstore: an object key is required")
	case len(key) > 1024:
		return errors.New("objectstore: an object key is longer than 1024 bytes")
	case strings.HasPrefix(key, "/"):
		return errors.New("objectstore: an object key cannot start with a slash")
	case strings.IndexFunc(key, controlRune) >= 0:
		return errors.New("objectstore: an object key cannot contain control characters")
	}
	for segment := range strings.SplitSeq(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("objectstore: an object key cannot contain empty, . or .. segments")
		}
	}
	return nil
}

// controlRune is what a URL path must not carry raw: a server may strip, split or refuse it before the bucket sees it.
func controlRune(r rune) bool { return r < ' ' || r == 0x7f }
