package declared

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// specVersion is the only declaration format this program reads. A document of another version is refused at load,
// because a member it adds may change what an older reader would do with it.
const specVersion = 1

// EnvVar names the directory of declaration files. It is read at startup, on every node, before the database is
// checked (internal/suppliers/README.md「声明式适配器」).
const EnvVar = "AXIOM_SUPPLIER_ADAPTERS"

// Adapter is a declaration compiled to the module's adapter contract.
type Adapter struct {
	kind    adapter.Kind
	native  string
	buckets []adapter.BucketSpec
	auth    authPlan
	detect  detectPlan
	account accountPlan
	offer   offerPlan
	keys    keysPlan
	charges chargesPlan
	calls   callPlan
	now     func() time.Time
}

type authPlan struct {
	fields   []adapter.LoginField
	hasToken bool
	header   string
	prefix   string
	query    string
	login    *loginPlan
}

type loginPlan struct {
	path      tmpl
	body      map[string]tmpl
	token     path
	expiresAt *path
	bucket    string
}

type detectPlan struct {
	path       tmpl
	header     string
	version    *path
	generation *path
	captcha    *path
	bucket     string
}

type accountPlan struct {
	path      tmpl
	balance   path
	unitHint  *path
	unitValue string
	funding   *fundingPlan
	bucket    string
}

type fundingPlan struct {
	path   path
	wallet map[string]bool
}

type offerPlan struct {
	groups      groupPlan
	models      modelPlan
	prices      *pricePlan
	formats     map[string]protocol.Format
	fallback    []protocol.Format // calls.formats, used when a group and a model state none
	fingerprint string            // the declaration this offer state belongs to
}

type groupPlan struct {
	path      tmpl
	items     path
	ref       path
	name      *path
	ratio     *path
	formats   *path
	skipRatio map[string]bool
	bucket    string
}

type modelPlan struct {
	path    tmpl
	items   path
	name    *path
	formats *path
	group   *path
	bucket  string
}

type pricePlan struct {
	path   tmpl
	items  path
	model  path
	rates  map[catalog.Item]path
	scale  *big.Rat
	bucket string
}

type keysPlan struct {
	list         keyListPlan
	secret       keySecretPlan
	create       *keyCreatePlan
	unrestricted bool
	policy       *keyPolicyPlan
}

type keyListPlan struct {
	path     tmpl
	pageSize int
	total    *path
	items    path
	ref      path
	group    *path
	name     *path
	status   *path
	statuses map[string]adapter.KeyStatus
	resetAt  *path
	bucket   string
}

type keySecretPlan struct {
	path   tmpl
	token  path
	bucket string
}

type keyCreatePlan struct {
	path   tmpl
	body   map[string]tmpl
	ref    path
	bucket string
}

type keyPolicyPlan struct {
	models    path
	expiresAt *path
}

type chargesPlan struct {
	path     tmpl
	pageSize int
	items    path
	ref      path
	key      path
	occurred path
	amount   path
	model    *path
	funding  *fundingPlan
	usage    map[catalog.Item]path
	filter   *filterPlan
	bucket   string
}

type filterPlan struct {
	path  path
	value string
}

// Compile checks a declaration and turns it into an adapter. Everything a call would otherwise discover late is
// checked here: a member this version does not know, a field path that does not parse, a placeholder an operation
// cannot fill, a bucket that is named but not declared, a cause the contract does not have, and a rule that would let a
// 5xx pass for a request the supplier never ran. A broken declaration therefore stops the node at startup, not a
// supplier's sync at its first read.
func Compile(spec Spec) (*Adapter, error) {
	a, c, err := newAdapter(spec)
	if err != nil {
		return nil, err
	}
	if err := a.read(c, spec); err != nil {
		return nil, err
	}
	raw, err := jsonx.MarshalCanonical(spec)
	if err != nil {
		return nil, fmt.Errorf("declaration fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	a.offer.fingerprint = hex.EncodeToString(sum[:])
	return a, nil
}

func newAdapter(spec Spec) (*Adapter, *compiler, error) {
	if spec.V != specVersion {
		return nil, nil, fmt.Errorf("version %d, want %d", spec.V, specVersion)
	}
	kind := adapter.Kind(spec.Kind)
	if !kind.Valid() {
		return nil, nil, fmt.Errorf("kind %q: must be lower-case letters and digits, starting with a letter", spec.Kind)
	}
	switch spec.NativeUnit {
	case "quota", "usd":
	default:
		return nil, nil, fmt.Errorf("native_unit %q: want quota or usd", spec.NativeUnit)
	}
	buckets, names, err := compileBuckets(spec.Buckets)
	if err != nil {
		return nil, nil, err
	}
	return &Adapter{kind: kind, native: spec.NativeUnit, buckets: buckets, now: time.Now}, &compiler{buckets: names}, nil
}

// read compiles each operational half. The calls are in one place so a plan that fails stops the load before the next
// one is read, and the error says which half it came from.
func (a *Adapter) read(c *compiler, spec Spec) error {
	var err error
	if a.auth, err = c.auth(spec.Auth); err != nil {
		return err
	}
	if a.detect, err = c.detect(spec.Detect); err != nil {
		return err
	}
	if a.detect.captcha != nil && !a.auth.hasToken {
		return errors.New("detect.captcha: a site that can show a CAPTCHA must also accept access_token")
	}
	if a.account, err = c.account(spec.Account); err != nil {
		return err
	}
	if a.offer, err = c.offer(spec.Offer); err != nil {
		return err
	}
	if a.keys, err = c.keys(spec.Keys); err != nil {
		return err
	}
	if a.charges, err = c.charges(spec.Charges); err != nil {
		return err
	}
	if a.calls, err = c.calls(spec.Calls); err != nil {
		return err
	}
	for from, f := range a.offer.formats {
		if !slices.Contains(a.calls.formats, f) {
			return fmt.Errorf("offer.formats: %q maps to a format calls.formats does not declare", from)
		}
	}
	a.offer.fallback = a.calls.formats
	return nil
}

// compiler holds what each operation's checks share: the bucket names and how to read a field path or a template.
type compiler struct {
	buckets map[string]bool
}

func compileBuckets(specs []Bucket) ([]adapter.BucketSpec, map[string]bool, error) {
	if len(specs) == 0 {
		return nil, nil, errors.New("buckets: declare at least one, named global")
	}
	names := make(map[string]bool, len(specs))
	out := make([]adapter.BucketSpec, 0, len(specs))
	for _, b := range specs {
		spec, err := compileBucket(b, names)
		if err != nil {
			return nil, nil, err
		}
		names[b.Name] = true
		out = append(out, spec)
	}
	if !names["global"] {
		return nil, nil, errors.New("buckets: no bucket named global")
	}
	return out, names, nil
}

func compileBucket(b Bucket, names map[string]bool) (adapter.BucketSpec, error) {
	if b.Name == "" || names[b.Name] {
		return adapter.BucketSpec{}, fmt.Errorf("bucket %q: a name is required and must be unique", b.Name)
	}
	per := adapter.CountedByIP
	switch b.Per {
	case "ip":
	case "account":
		per = adapter.CountedByAccount
	default:
		return adapter.BucketSpec{}, fmt.Errorf("bucket %q: per is %q, want ip or account", b.Name, b.Per)
	}
	window, err := time.ParseDuration(b.Window)
	if err != nil || window <= 0 {
		return adapter.BucketSpec{}, fmt.Errorf("bucket %q: window %q is not a positive duration", b.Name, b.Window)
	}
	if b.Limit <= 0 {
		return adapter.BucketSpec{}, fmt.Errorf("bucket %q: limit %d is not positive", b.Name, b.Limit)
	}
	return adapter.BucketSpec{Name: b.Name, Per: per, Window: window, Limit: b.Limit}, nil
}

func (c *compiler) bucket(what, name string) (string, error) {
	if name == "" {
		name = "global"
	}
	if !c.buckets[name] {
		return "", fmt.Errorf("%s: bucket %q is not declared", what, name)
	}
	return name, nil
}

func (*compiler) path(what, text string) (path, error) {
	p, err := parsePath(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return p, nil
}

func (c *compiler) optionalPath(what, text string) (*path, error) {
	if text == "" {
		return nil, nil //nolint:nilnil // the declaration leaves the field out
	}
	p, err := c.path(what, text)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (*compiler) template(what, text string, variables ...string) (tmpl, error) {
	t, err := parseTemplate(what, text, variables...)
	if err != nil {
		return tmpl{}, err
	}
	return t, nil
}

func (*compiler) pathTemplate(what, text string, variables ...string) (tmpl, error) {
	t, err := pathTemplate(what, text, variables...)
	if err != nil {
		return tmpl{}, err
	}
	return t, nil
}

func (c *compiler) detect(spec Detect) (detectPlan, error) {
	var d detectPlan
	var err error
	if d.path, err = c.pathTemplate("detect.path", spec.Path); err != nil {
		return d, err
	}
	if d.bucket, err = c.bucket("detect", spec.Bucket); err != nil {
		return d, err
	}
	if spec.Header != "" {
		if !headerName(spec.Header) {
			return d, fmt.Errorf("detect.header: %q is not a header name", spec.Header)
		}
		d.header = http.CanonicalHeaderKey(spec.Header)
	}
	if d.version, err = c.optionalPath("detect.version", spec.Version); err != nil {
		return d, err
	}
	if d.generation, err = c.optionalPath("detect.generation", spec.Generation); err != nil {
		return d, err
	}
	if d.captcha, err = c.optionalPath("detect.captcha", spec.Captcha); err != nil {
		return d, err
	}
	if d.header == "" && d.version == nil && d.generation == nil && d.captcha == nil {
		return d, errors.New("detect: declare a header or a field that only this system answers, so the probe does not claim every site")
	}
	return d, nil
}

func (c *compiler) account(spec Account) (accountPlan, error) {
	var a accountPlan
	var err error
	if a.path, err = c.pathTemplate("account.path", spec.Path); err != nil {
		return a, err
	}
	if a.balance, err = c.path("account.balance", spec.Balance); err != nil {
		return a, err
	}
	if a.unitHint, err = c.optionalPath("account.unit_hint", spec.UnitHint); err != nil {
		return a, err
	}
	if spec.UnitHint != "" && spec.UnitValue != "" {
		return a, errors.New("account: declare unit_hint or unit_hint_value, not both")
	}
	a.unitValue = spec.UnitValue
	if a.bucket, err = c.bucket("account", spec.Bucket); err != nil {
		return a, err
	}
	if a.funding, err = c.funding("account.funding", spec.Funding); err != nil {
		return a, err
	}
	return a, nil
}

func (c *compiler) funding(what string, spec *Funding) (*fundingPlan, error) {
	if spec == nil {
		return nil, nil //nolint:nilnil // the declaration does not read funding
	}
	p, err := c.path(what+".path", spec.Path)
	if err != nil {
		return nil, err
	}
	if len(spec.Wallet) == 0 {
		return nil, fmt.Errorf("%s.wallet: declare at least one value", what)
	}
	wallet := make(map[string]bool, len(spec.Wallet))
	for _, v := range spec.Wallet {
		if v == "" || wallet[v] {
			return nil, fmt.Errorf("%s.wallet: %q is empty or listed twice", what, v)
		}
		wallet[v] = true
	}
	return &fundingPlan{path: p, wallet: wallet}, nil
}

// has reports whether the template names a variable.
func (t tmpl) has(name string) bool {
	for _, p := range t.parts {
		if p.variable == name {
			return true
		}
	}
	return false
}

var (
	formatNames = map[string]protocol.Format{
		"messages": protocol.Messages, "responses": protocol.Responses, "chat": protocol.Chat,
		"embeddings": protocol.Embeddings, "rerank": protocol.Rerank, "completions": protocol.Completions,
	}
	keyStatuses = map[string]adapter.KeyStatus{
		"active": adapter.KeyActive, "disabled": adapter.KeyDisabled,
		"quota_exhausted": adapter.KeyQuotaExhausted, "expired": adapter.KeyExpired,
	}
)

// headerName accepts an HTTP field-name token (RFC 9110 section 5.1). Parentheses, slashes and other separators are
// not field-name characters, even though net/http would canonicalize some of them without complaint.
func headerName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}
