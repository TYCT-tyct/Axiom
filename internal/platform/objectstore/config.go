package objectstore

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"axiom/internal/platform/config"
	"axiom/internal/platform/secret"
)

// The variables that configure the bucket. They are named here, where they are read, so the errors and the
// documentation spell them the same way.
const (
	endpointVar  = "AXIOM_OBJECTSTORE_ENDPOINT"
	bucketVar    = "AXIOM_OBJECTSTORE_BUCKET"
	regionVar    = "AXIOM_OBJECTSTORE_REGION"
	accessKeyVar = "AXIOM_OBJECTSTORE_ACCESS_KEY_ID"
	secretKeyVar = "AXIOM_OBJECTSTORE_SECRET_ACCESS_KEY"
	pathStyleVar = "AXIOM_OBJECTSTORE_PATH_STYLE"

	// defaultRegion is where S3-compatible servers that do not care about a region expect the credential scope to
	// say us-east-1; AWS itself uses the bucket's region.
	defaultRegion = "us-east-1"
)

// Config is one S3-compatible bucket: where it is, which region signs the requests and the credential pair. The
// zero value means object storage is disabled, which is the state of a deployment that has not configured one.
type Config struct {
	Endpoint        string // scheme and host, optionally with a base path; no trailing slash
	Bucket          string
	Region          string
	AccessKeyID     string
	SecretAccessKey secret.Secret // never printed: the type redacts it in every formatter
	PathStyle       bool          // true addresses the bucket in the path, as SeaweedFS, R2 and MinIO expect
}

// Configured reports whether the deployment has an object store. A node with none of the variables set has the zero
// Config; a partly set one fails ReadConfig instead of reaching this state.
func (c Config) Configured() bool { return c.Endpoint != "" }

// ReadConfig reads the bucket's settings from env. With none of them set, object storage is disabled and the zero
// Config is returned, so a node without an archive bucket runs every other feature. Setting any one of them arms the
// whole configuration: a partly set one is a startup error, because the node would otherwise discover at the first
// upload that it has no credentials.
//
// Errors name the variable and the shape wanted, never the value: the secret key is a credential, and a credential
// assigned to the wrong variable must not reach the logs.
func ReadConfig(env *config.Env) (Config, error) {
	raw := rawConfig{
		endpoint:  env.String(endpointVar, ""),
		bucket:    env.String(bucketVar, ""),
		region:    env.String(regionVar, ""),
		accessKey: env.String(accessKeyVar, ""),
		secretKey: env.String(secretKeyVar, ""),
		pathStyle: env.String(pathStyleVar, ""),
	}
	if raw == (rawConfig{}) {
		return Config{}, nil
	}
	return raw.parse()
}

// rawConfig is the six values as the environment spells them, before any check.
type rawConfig struct {
	endpoint, bucket, region, accessKey, secretKey, pathStyle string
}

// parse checks the whole configuration at once and reports every problem it finds, so a node started with half of it
// hears about all of it at startup rather than at the first upload.
func (r rawConfig) parse() (Config, error) {
	cfg := Config{
		Endpoint:        strings.TrimSuffix(strings.TrimSpace(r.endpoint), "/"),
		Bucket:          strings.TrimSpace(r.bucket),
		Region:          strings.TrimSpace(r.region),
		AccessKeyID:     r.accessKey,
		SecretAccessKey: secret.New(r.secretKey),
		PathStyle:       true,
	}
	var errs []error
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	switch r.pathStyle {
	case "":
	case "true":
	case "false":
		cfg.PathStyle = false
	default:
		errs = append(errs, fmt.Errorf("%s: want true or false", pathStyleVar))
	}
	if _, err := cfg.check(); err != nil {
		errs = append(errs, err)
	}
	return cfg, errors.Join(errs...)
}

// check reports every problem in c and returns the parsed endpoint.
func (c Config) check() (*url.URL, error) {
	var errs []error
	endpoint, endpointErr := parseEndpoint(c.Endpoint)
	errs = append(errs, endpointErr)
	if err := checkBucket(c.Bucket); err != nil {
		errs = append(errs, err)
	}
	if c.Region == "" {
		errs = append(errs, fmt.Errorf("%s: required", regionVar))
	} else if !plainToken(c.Region) {
		errs = append(errs, fmt.Errorf("%s: want a region such as us-east-1", regionVar))
	}
	switch {
	case c.AccessKeyID == "" && c.SecretAccessKey.Reveal() == "":
		errs = append(errs, fmt.Errorf("%s and %s: required", accessKeyVar, secretKeyVar))
	case c.AccessKeyID == "":
		errs = append(errs, fmt.Errorf("%s: required when %s is set", accessKeyVar, secretKeyVar))
	case c.SecretAccessKey.Reveal() == "":
		errs = append(errs, fmt.Errorf("%s: required when %s is set", secretKeyVar, accessKeyVar))
	}
	if endpoint != nil && !c.PathStyle && net.ParseIP(endpoint.Hostname()) != nil {
		errs = append(errs, fmt.Errorf("%s: a virtual-host bucket needs a DNS name, not an IP address; set %s=true",
			endpointVar, pathStyleVar))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return endpoint, nil
}

// parseEndpoint accepts an absolute http(s) address without user information, query or fragment. A base path is
// allowed because some gateways mount the S3 API under one; every request is made below it.
func parseEndpoint(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%s: required to use object storage", endpointVar)
	}
	if strings.IndexFunc(raw, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
		return nil, fmt.Errorf("%s: want an address such as https://s3.example.com", endpointVar)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: want an absolute http or https address such as https://s3.example.com", endpointVar)
	}
	if err := endpointShape(u); err != nil {
		return nil, err
	}
	if u.Path != "" && u.Path != "/" && !strings.HasPrefix(u.Path, "/") {
		return nil, fmt.Errorf("%s: want a base path such as /s3", endpointVar)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// endpointShape refuses an address that is not an absolute http(s) URL with a host and nothing that would make the
// request address ambiguous.
func endpointShape(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s: want an absolute http or https address such as https://s3.example.com", endpointVar)
	}
	return nil
}

// checkBucket refuses what cannot be a path or host label, not the naming policy of one vendor: every S3 server
// enforces its own, and this side cannot know which one the deployment chose.
func checkBucket(bucket string) error {
	switch {
	case bucket == "":
		return fmt.Errorf("%s: required to use object storage", bucketVar)
	case len(bucket) > 255:
		return fmt.Errorf("%s: want at most 255 characters", bucketVar)
	case strings.ContainsAny(bucket, "/ \t\r\n"):
		return fmt.Errorf("%s: want a bucket name without a slash or space", bucketVar)
	case bucket == "." || bucket == "..":
		return fmt.Errorf("%s: want a bucket name, not a dot segment", bucketVar)
	}
	return nil
}

// plainToken accepts the characters every S3 implementation allows in a region name.
func plainToken(value string) bool {
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return true
}
