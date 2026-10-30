package objectstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// hashBody returns the payload hash and size of body, and rewinds it for the request that follows. A nil body is the
// empty payload. Retries rewind the same body again, so a large object is sent from the start each time.
func hashBody(body io.ReadSeeker) (string, int64, error) {
	if body == nil {
		return emptySHA256, 0, nil
	}
	hash := sha256.New()
	size, err := io.Copy(hash, body)
	if err != nil {
		return "", 0, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
