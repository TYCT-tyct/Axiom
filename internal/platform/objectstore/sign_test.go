package objectstore

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
)

// signedAt is the instant the vectors were made at, so the signatures are reproducible.
var signedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// The test's own credentials, used only to reproduce botocore's vectors.
const (
	vectorID  = "AKIAEXAMPLE"
	vectorKey = "objectstore-test-secret"
)

// The expected Authorization values come from botocore's S3 signer for the same request, date and credentials. They
// pin the two things an S3 implementation actually checks: the escaped path and the canonical query and headers.
//
//nolint:cyclop,gocognit // one table drives every signature vector; the cases are the test
func TestSignsLikeAnS3Client(t *testing.T) {
	for _, c := range []struct {
		name    string
		cfg     Config
		req     request
		want    string
		payload string
	}{
		{
			name:    "get with a space in the key",
			cfg:     pathStyle(),
			req:     request{method: "GET", key: "archive/2026/a b.txt", payloadHash: emptySHA256},
			payload: emptySHA256,
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-east-1/s3/aws4_request, " +
				"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
				"Signature=fb1285cb0bf25e24bdeada3bee55edbf543d0692443752d7d83909d9fd0e2f32",
		},
		{
			name: "put with a content type",
			cfg:  pathStyle(),
			req: request{
				method: "PUT", key: "archive/x.bin", body: strings.NewReader("hello world"), size: 11,
				contentType: "application/octet-stream", payloadHash: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
			},
			payload: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-east-1/s3/aws4_request, " +
				"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, " +
				"Signature=c6b813b6ec0a50f368d57abdb131fa89bb3aad9d4a8371b3f3428004a4d8950f",
		},
		{
			name:    "list with a query",
			cfg:     pathStyle(),
			req:     request{method: "GET", query: url.Values{"list-type": {"2"}, "max-keys": {"1000"}, "prefix": {"archive/"}}, payloadHash: emptySHA256},
			payload: emptySHA256,
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-east-1/s3/aws4_request, " +
				"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
				"Signature=8b0e59ba7006aeb3783f8b74702b6ac8fc260934936fd466e7f140c5f329fb54",
		},
		{
			name:    "head bucket",
			cfg:     pathStyle(),
			req:     request{method: "HEAD", payloadHash: emptySHA256},
			payload: emptySHA256,
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-east-1/s3/aws4_request, " +
				"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
				"Signature=8f4fcaf58fd070ac2eb570a5063f9e5e0307ec5054baa772c7dbb8478aca8b65",
		},
		{
			name:    "escaped key",
			cfg:     pathStyle(),
			req:     request{method: "GET", key: "a~b+c:d e.txt", payloadHash: emptySHA256},
			payload: emptySHA256,
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-east-1/s3/aws4_request, " +
				"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
				"Signature=ef1e29157c21e318fecbed136c9394969eda59498942d624172d3aefc365f275",
		},
		{
			name:    "unicode key",
			cfg:     pathStyle(),
			req:     request{method: "GET", key: "中文.txt", payloadHash: emptySHA256},
			payload: emptySHA256,
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-east-1/s3/aws4_request, " +
				"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
				"Signature=f517f0397d208236a4ec50defa32438a39efd60a0d23b04c87fcaf19f18d2f9c",
		},
		{
			name: "virtual host and another region",
			cfg: Config{
				Endpoint: "https://s3.us-west-2.amazonaws.com", Bucket: "axiom", Region: "us-west-2",
				AccessKeyID: vectorID, SecretAccessKey: secret.New(vectorKey), PathStyle: false,
			},
			req:     request{method: "GET", key: "archive/x.bin", payloadHash: emptySHA256},
			payload: emptySHA256,
			want: "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260102/us-west-2/s3/aws4_request, " +
				"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
				"Signature=14944fe78215ac9e160f113cf451414230015ea20fb8b20e098632eb0b103823",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, err := Open(c.cfg)
			if err != nil {
				t.Fatal(err)
			}
			client.now = func() time.Time { return signedAt }
			got, err := client.request(context.Background(), c.req)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL.EscapedPath() != got.URL.RawPath {
				t.Fatalf("escaped path %q is not the raw path %q", got.URL.EscapedPath(), got.URL.RawPath)
			}
			if c.name == "escaped key" {
				if want := "/axiom/a~b%2Bc%3Ad%20e.txt"; got.URL.RawPath != want {
					t.Fatalf("raw path = %q, want %q", got.URL.RawPath, want)
				}
			}
			if c.name == "unicode key" {
				if want := "/axiom/%E4%B8%AD%E6%96%87.txt"; got.URL.RawPath != want {
					t.Fatalf("raw path = %q, want %q", got.URL.RawPath, want)
				}
			}
			if auth := got.Header.Get("Authorization"); auth != c.want {
				t.Fatalf("Authorization =\n%s\nwant\n%s", auth, c.want)
			}
			if payload := got.Header.Get("X-Amz-Content-Sha256"); payload != c.payload {
				t.Fatalf("payload hash = %q, want %q", payload, c.payload)
			}
			if host := got.Header.Get("X-Amz-Date"); host != "20260102T030405Z" {
				t.Fatalf("x-amz-date = %q", host)
			}
			if ua := got.Header.Get("User-Agent"); ua != "Axiom" {
				t.Fatalf("User-Agent = %q", ua)
			}
		})
	}
}

func TestCanonicalQueryEncodesAndSorts(t *testing.T) {
	got := canonicalQuery(url.Values{
		"prefix":    {"a b/c"},
		"max-keys":  {"10"},
		"list-type": {"2"},
	})
	if want := "list-type=2&max-keys=10&prefix=a%20b%2Fc"; got != want {
		t.Fatalf("canonicalQuery = %q, want %q", got, want)
	}
}

func pathStyle() Config {
	return Config{
		Endpoint: "http://127.0.0.1:8333", Bucket: "axiom", Region: "us-east-1",
		AccessKeyID: vectorID, SecretAccessKey: secret.New(vectorKey), PathStyle: true,
	}
}
