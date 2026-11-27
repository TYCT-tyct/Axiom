package bedrock

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// AWS Signature Version 4 (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html),
// as a service other than S3 takes it: the canonical request names the method, the path with each segment encoded
// again, the query sorted, every header the request carries and the payload's SHA-256; the string to sign names the
// time, the scope (date, region, service) and the canonical request's hash; the signing key is the secret chained by
// HMAC-SHA256 through the scope. TestSignatureMatchesAWSVectors checks it against AWS's published examples.

const (
	algorithm  = "AWS4-HMAC-SHA256"
	amzDate    = "20060102T150405Z"
	scopeDate  = "20060102"
	terminator = "aws4_request"
)

// sign puts X-Amz-Date and Authorization on req for service in region at at with k's access key, over payload, which
// is the body req sends. Every header req carries is signed, with Host; the outbound client sets only User-Agent
// after, which is not.
func sign(req *http.Request, payload []byte, k credential, region, service string, at time.Time) {
	t := at.UTC()
	req.Header.Set("X-Amz-Date", t.Format(amzDate))
	names, headers := canonicalHeaders(req)
	sum := sha256.Sum256(payload)
	canonical := strings.Join([]string{
		req.Method, canonicalPath(req.URL), canonicalQuery(req.URL), headers, names, hex.EncodeToString(sum[:]),
	}, "\n")
	scope := t.Format(scopeDate) + "/" + region + "/" + service + "/" + terminator
	hashed := sha256.Sum256([]byte(canonical))
	toSign := algorithm + "\n" + t.Format(amzDate) + "\n" + scope + "\n" + hex.EncodeToString(hashed[:])
	key := signingKey(k.SecretAccessKey, t.Format(scopeDate), region, service)
	req.Header.Set("Authorization", algorithm+" Credential="+k.AccessKeyID+"/"+scope+", SignedHeaders="+names+
		", Signature="+hex.EncodeToString(mac(key, toSign)))
}

// signingKey is the secret chained through the scope.
func signingKey(secret, date, region, service string) []byte {
	return mac(mac(mac(mac([]byte("AWS4"+secret), date), region), service), terminator)
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalHeaders is the signed header names, lower case, sorted and joined by ';', and their canonical block: each
// name:value with the value's spaces trimmed and runs of them made one, a header's values joined by ',', and a newline
// after each.
func canonicalHeaders(req *http.Request) (names, block string) {
	values := map[string]string{"host": req.URL.Host}
	for name, vs := range req.Header {
		if lower := strings.ToLower(name); lower != "authorization" && lower != "user-agent" {
			trimmed := make([]string, len(vs))
			for i, v := range vs {
				trimmed[i] = strings.Join(strings.Fields(v), " ")
			}
			values[lower] = strings.Join(trimmed, ",")
		}
	}
	sorted := make([]string, 0, len(values))
	for name := range values {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	var b strings.Builder
	for _, name := range sorted {
		b.WriteString(name + ":" + values[name] + "\n")
	}
	return strings.Join(sorted, ";"), b.String()
}

// canonicalPath is the path as sent with each segment encoded once more, which a service other than S3 signs.
func canonicalPath(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = escape(s)
	}
	return strings.Join(segments, "/")
}

// canonicalQuery is the query's parameters encoded and sorted by name, then value.
func canonicalQuery(u *url.URL) string {
	var pairs [][2]string
	for name, vs := range u.Query() {
		for _, v := range vs {
			pairs = append(pairs, [2]string{escape(name), escape(v)})
		}
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	joined := make([]string, len(pairs))
	for i, p := range pairs {
		joined[i] = p[0] + "=" + p[1]
	}
	return strings.Join(joined, "&")
}

// escape is AWS's URI encoding: every byte but the unreserved letters, digits and -_.~ as %XY, in upper case.
func escape(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}
