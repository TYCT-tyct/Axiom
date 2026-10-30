package objectstore

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/platform/config"
	"axiom/internal/platform/secret"
)

// env builds the environment from pairs, as os.Environ would spell them.
func env(pairs ...string) []string {
	out := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, pairs[i]+"="+pairs[i+1])
	}
	return out
}

func TestReadConfigDisabled(t *testing.T) {
	cfg, err := ReadConfig(config.New(nil))
	if err != nil || cfg.Configured() {
		t.Fatalf("no variables: cfg = %+v, err = %v, want the zero configuration", cfg, err)
	}
}

//nolint:cyclop,gocognit // the table is the test, one row per configuration the reader must get right
func TestReadConfig(t *testing.T) {
	for _, c := range []struct {
		name      string
		env       []string
		want      Config
		wantError string
	}{
		{
			name: "all values",
			env: env(
				endpointVar, "https://s3.example.com",
				bucketVar, "archive",
				regionVar, "eu-central-1",
				accessKeyVar, "AKIAEXAMPLE",
				secretKeyVar, "secret",
				pathStyleVar, "false",
			),
			want: Config{
				Endpoint: "https://s3.example.com", Bucket: "archive", Region: "eu-central-1",
				AccessKeyID: "AKIAEXAMPLE", PathStyle: false,
			},
		},
		{
			name: "defaults",
			env: env(
				endpointVar, "http://seaweedfs:8333/",
				bucketVar, "archive",
				accessKeyVar, "AKIAEXAMPLE",
				secretKeyVar, "secret",
			),
			want: Config{Endpoint: "http://seaweedfs:8333", Bucket: "archive", Region: defaultRegion, AccessKeyID: "AKIAEXAMPLE", PathStyle: true},
		},
		{
			name:      "only the endpoint",
			env:       env(endpointVar, "https://s3.example.com"),
			wantError: bucketVar + ": required",
		},
		{
			name:      "no credentials",
			env:       env(endpointVar, "https://s3.example.com", bucketVar, "archive"),
			wantError: accessKeyVar + " and " + secretKeyVar + ": required",
		},
		{
			name:      "half a credential pair",
			env:       env(endpointVar, "https://s3.example.com", bucketVar, "archive", accessKeyVar, "AKIAEXAMPLE"),
			wantError: secretKeyVar + ": required",
		},
		{
			name:      "bad endpoint",
			env:       env(endpointVar, "s3.example.com", bucketVar, "archive", accessKeyVar, "A", secretKeyVar, "S"),
			wantError: endpointVar + ": want an absolute http or https address",
		},
		{
			name:      "user information in the endpoint",
			env:       env(endpointVar, "https://user:pass@s3.example.com", bucketVar, "archive", accessKeyVar, "A", secretKeyVar, "S"),
			wantError: endpointVar + ": want an absolute http or https address",
		},
		{
			name:      "bad path style",
			env:       env(endpointVar, "https://s3.example.com", bucketVar, "archive", accessKeyVar, "A", secretKeyVar, "S", pathStyleVar, "yes"),
			wantError: pathStyleVar + ": want true or false",
		},
		{
			name:      "virtual host on an IP",
			env:       env(endpointVar, "http://127.0.0.1:8333", bucketVar, "archive", accessKeyVar, "A", secretKeyVar, "S", pathStyleVar, "false"),
			wantError: endpointVar + ": a virtual-host bucket needs a DNS name",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := ReadConfig(config.New(c.env))
			if c.wantError == "" {
				if err != nil {
					t.Fatalf("ReadConfig: %v", err)
				}
				if cfg.Configured() != true || cfg.Endpoint != c.want.Endpoint || cfg.Bucket != c.want.Bucket ||
					cfg.Region != c.want.Region || cfg.AccessKeyID != c.want.AccessKeyID || cfg.PathStyle != c.want.PathStyle {
					t.Fatalf("cfg = %+v, want %+v", cfg, c.want)
				}
				if cfg.SecretAccessKey.Reveal() == "" {
					t.Fatal("the secret key was not read")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantError) {
				t.Fatalf("err = %v, want %q", err, c.wantError)
			}
		})
	}
}

func TestConfigErrorsNeverPrintTheSecret(t *testing.T) {
	_, err := ReadConfig(config.New(env(endpointVar, "https://s3.example.com", secretKeyVar, "do-not-print-me")))
	if err == nil {
		t.Fatal("a configuration with no bucket was accepted")
	}
	if strings.Contains(err.Error(), "do-not-print-me") {
		t.Fatalf("the error prints the secret: %v", err)
	}
}

func TestOpenRefusesTheZeroConfiguration(t *testing.T) {
	if _, err := Open(Config{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Open(zero) = %v, want ErrNotConfigured", err)
	}
}

func TestOpenCarriesTheBucketInTheHostForVirtualStyle(t *testing.T) {
	c, err := Open(Config{
		Endpoint: "https://s3.example.com:9000", Bucket: "archive", Region: "us-east-1",
		AccessKeyID: "A", SecretAccessKey: secret.New("S"), PathStyle: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.endpoint.Host; got != "archive.s3.example.com:9000" {
		t.Fatalf("host = %q, want the bucket in front of the endpoint's host and port", got)
	}
}
