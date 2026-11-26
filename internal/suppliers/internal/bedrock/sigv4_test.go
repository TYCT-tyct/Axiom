package bedrock

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

// awsExample is the access key every one of AWS's published signing examples uses.
var awsExample = keys{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}

// TestSignatureMatchesAWSVectors: requests signed as AWS's own examples have them come out with the published
// signatures: the get-vanilla, post-vanilla and get-vanilla-query-order-key-case cases of the Signature Version 4 test
// suite, and the IAM ListUsers request of the IAM user guide, which signs a Content-Type and a query.
func TestSignatureMatchesAWSVectors(t *testing.T) {
	t.Parallel()
	at := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, method, url, service, contentType, want string
	}{
		{"get-vanilla", http.MethodGet, "https://example.amazonaws.com/", "service", "",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"post-vanilla", http.MethodPost, "https://example.amazonaws.com/", "service", "",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"get-vanilla-query-order-key-case", http.MethodGet, "https://example.amazonaws.com/?Param2=value2&Param1=value1", "service", "",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
		{"iam-list-users", http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", "iam", "application/x-www-form-urlencoded; charset=utf-8",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		sign(req, nil, awsExample, "us-east-1", tc.service, at)
		if got := req.Header.Get("Authorization"); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
	// The signing key of the IAM user guide's example.
	if got := hex.EncodeToString(signingKey(awsExample.SecretAccessKey, "20150830", "us-east-1", "iam")); got != "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9" {
		t.Errorf("the signing key is %s", got)
	}
}

// TestSignedPathEncodesTheModelTwice: a model's ID has a colon, which the path sends encoded once and the signature
// encodes again, as a service other than S3 signs it.
func TestSignedPathEncodesTheModelTwice(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, invokeURL("us-east-1", "anthropic.claude-x-v1:0", false), strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.EscapedPath(); got != "/model/anthropic.claude-x-v1%3A0/invoke" {
		t.Errorf("the path sent is %s", got)
	}
	if got := canonicalPath(req.URL); got != "/model/anthropic.claude-x-v1%253A0/invoke" {
		t.Errorf("the path signed is %s", got)
	}
}
