package objectstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	amzDateFormat = "20060102T150405Z"
	dayFormat     = "20060102"
	algorithm     = "AWS4-HMAC-SHA256"
	emptySHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// sign adds the SigV4 headers of one S3 request. The canonical URI is the request's already-escaped path, which is
// what S3 signs (unlike the other AWS services, it does not normalize or encode it again).
func (c *Client) sign(req *http.Request, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format(amzDateFormat)
	day := now.UTC().Format(dayFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	headers := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if req.Header.Get("Content-Type") != "" {
		headers = append(headers, "content-type")
	}
	sort.Strings(headers)

	var canonical strings.Builder
	for _, name := range headers {
		value := req.Header.Get(name)
		if name == "host" {
			value = req.URL.Host
		}
		canonical.WriteString(name)
		canonical.WriteByte(':')
		canonical.WriteString(canonicalValue(value))
		canonical.WriteByte('\n')
	}
	signed := strings.Join(headers, ";")
	request := req.Method + "\n" + req.URL.EscapedPath() + "\n" + req.URL.RawQuery + "\n" +
		canonical.String() + "\n" + signed + "\n" + payloadHash

	scope := day + "/" + c.region + "/s3/aws4_request"
	toSign := algorithm + "\n" + amzDate + "\n" + scope + "\n" + sumHex([]byte(request))
	key := signingKey(c.secretKey.Reveal(), day, c.region)
	req.Header.Set("Authorization", algorithm+" Credential="+c.accessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hmacHex(key, []byte(toSign)))
}

// canonicalValue trims a header value and folds its internal whitespace, as SigV4 defines it. The values set here are
// simple, so this only keeps a caller-supplied content type from changing the signature.
func canonicalValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func signingKey(secret, day, region string) []byte {
	date := hmacSum([]byte("AWS4"+secret), []byte(day))
	area := hmacSum(date, []byte(region))
	service := hmacSum(area, []byte("s3"))
	return hmacSum(service, []byte("aws4_request"))
}

func hmacSum(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func hmacHex(key, data []byte) string { return hex.EncodeToString(hmacSum(key, data)) }

// sumHex is the lower-case SHA-256 of data, the form SigV4 writes hashes in.
func sumHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashBody returns the payload hash and size of body, and rewinds it for the request that follows. It starts at the
// beginning whatever position the caller left the reader in, so the hash and the bytes sent are the same range. A nil
// body is the empty payload. It reads at most limit+1 bytes and returns ErrTooLarge past the limit; a Read that itself
// blocks cannot be interrupted here, so a body that may block must be closed by its owner.
func hashBody(ctx context.Context, body io.ReadSeeker, limit int64) (string, int64, error) {
	if body == nil {
		return emptySHA256, 0, nil
	}
	if err := requireDeadline(ctx); err != nil {
		return "", 0, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, err := hashChunks(ctx, hash, body, limit)
	if err != nil {
		return "", 0, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// hashChunks copies at most limit+1 bytes into hash, checking ctx between reads and stopping as soon as the body is
// over the limit.
//
//nolint:gocognit // one loop enforces cancellation, the limit, EOF and write errors in that order
func hashChunks(ctx context.Context, hash io.Writer, body io.Reader, limit int64) (int64, error) {
	buf := make([]byte, 32<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		want := int64(len(buf))
		if remaining := limit + 1 - size; remaining < want {
			want = remaining
		}
		if want <= 0 {
			return 0, ErrTooLarge
		}
		n, err := body.Read(buf[:want])
		if n > 0 {
			size += int64(n)
			if size > limit {
				return 0, ErrTooLarge
			}
			if _, writeErr := hash.Write(buf[:n]); writeErr != nil {
				return 0, writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			return size, nil
		}
		if err != nil {
			return 0, err
		}
	}
}
