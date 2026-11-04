package declared

// Spec is one declared adapter as a JSON document (v1). Every member is checked at load (compile.go); the types here
// only name the document. A member with no value is absent, not defaulted, unless the comment says otherwise.
type Spec struct {
	V          int      `json:"v"`
	Kind       string   `json:"kind"`
	NativeUnit string   `json:"native_unit"` // quota or usd: the unit the balance is counted in
	Buckets    []Bucket `json:"buckets"`
	Auth       Auth     `json:"auth"`
	Detect     Detect   `json:"detect"`
	Account    Account  `json:"account"`
	Offer      Offer    `json:"offer"`
	Keys       Keys     `json:"keys"`
	Charges    Charges  `json:"charges"`
	Calls      Calls    `json:"calls"`
}

// Bucket is one rate limit of the relay, as the adapter counts its own requests against it.
type Bucket struct {
	Name   string `json:"name"`
	Per    string `json:"per"`    // "ip" or "account"
	Window string `json:"window"` // a Go duration, such as "60s"
	Limit  int    `json:"limit"`
}

// Auth is how the management requests authenticate. A login operation is optional: without it the person supplies an
// access token, and with it the adapter logs in with the account's email and password.
type Auth struct {
	Fields []string `json:"fields"` // access_token, email and password, in the combinations compile accepts
	Header string   `json:"header,omitempty"`
	Prefix string   `json:"prefix,omitempty"`
	Query  string   `json:"query,omitempty"`
	Login  *Login   `json:"login,omitempty"`
}

// Login is the request that turns the account credentials into a token.
type Login struct {
	Path      string            `json:"path"`
	Body      map[string]string `json:"body,omitempty"`
	Token     string            `json:"token"`
	ExpiresAt string            `json:"expires_at,omitempty"`
	Bucket    string            `json:"bucket,omitempty"`
}

// Detect is the probe that recognises the system. At least one of Header and the three fields must be declared, so a
// declaration cannot claim every site that answers 200.
type Detect struct {
	Path       string `json:"path"`
	Header     string `json:"header,omitempty"`
	Version    string `json:"version,omitempty"`
	Generation string `json:"generation,omitempty"`
	Captcha    string `json:"captcha,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
}

// Account reads the balance, and optionally what the relay says a native unit is and where calls are paid from.
type Account struct {
	Path      string   `json:"path"`
	Balance   string   `json:"balance"`
	UnitHint  string   `json:"unit_hint,omitempty"`
	UnitValue string   `json:"unit_hint_value,omitempty"`
	Funding   *Funding `json:"funding,omitempty"`
	Bucket    string   `json:"bucket,omitempty"`
}

// Funding maps a value the relay reports to "the wallet" or "not the wallet".
type Funding struct {
	Path   string   `json:"path"`
	Wallet []string `json:"wallet"`
}

// Offer is the read of groups, their models and the prices that make cost plans.
type Offer struct {
	Groups  Groups            `json:"groups"`
	Models  Models            `json:"models"`
	Prices  *Prices           `json:"prices,omitempty"`
	Formats map[string]string `json:"formats,omitempty"` // a value the relay uses to one of the protocol format names
}

// Groups reads the groups.
type Groups struct {
	Path      string   `json:"path"`
	Items     string   `json:"items"`
	Ref       string   `json:"ref"`
	Name      string   `json:"name,omitempty"`
	Ratio     string   `json:"ratio,omitempty"`
	Formats   string   `json:"formats,omitempty"`
	SkipRatio []string `json:"skip_ratio,omitempty"` // ratios that mean "not sold to us", such as new-api's auto
	Bucket    string   `json:"bucket,omitempty"`
}

// Models reads the models of one group.
type Models struct {
	Path    string `json:"path"`  // must hold the {group} placeholder
	Items   string `json:"items"` // an array of names or of objects; Name is empty for names
	Name    string `json:"name,omitempty"`
	Formats string `json:"formats,omitempty"`
	Group   string `json:"group,omitempty"` // the group value to substitute; the group's ref when empty
	Bucket  string `json:"bucket,omitempty"`
}

// Prices reads the price list once, keyed by model name.
type Prices struct {
	Path   string            `json:"path"`
	Items  string            `json:"items"`
	Model  string            `json:"model"`
	Rates  map[string]string `json:"rates"`           // catalog item name to a field path holding its price
	Scale  string            `json:"scale,omitempty"` // a decimal multiplier from the listed unit to one token, default 1
	Bucket string            `json:"bucket,omitempty"`
}

// Keys is the read and the optional making of the keys Axiom calls with.
type Keys struct {
	List         KeyList    `json:"list"`
	Secret       KeySecret  `json:"secret"`
	Create       *KeyCreate `json:"create,omitempty"`
	Unrestricted bool       `json:"unrestricted,omitempty"`
	Policy       *KeyPolicy `json:"policy,omitempty"`
}

// KeyList reads the account's keys. A page is read only when the path names {page} and {size}; otherwise the whole
// listing is one answer.
type KeyList struct {
	Path     string            `json:"path"`
	PageSize int               `json:"page_size,omitempty"`
	Total    string            `json:"total,omitempty"`
	Items    string            `json:"items"`
	Ref      string            `json:"ref"`
	Group    string            `json:"group,omitempty"`
	Name     string            `json:"name,omitempty"`
	Status   string            `json:"status,omitempty"`
	Statuses map[string]string `json:"statuses,omitempty"` // the relay's status text to active, disabled, quota_exhausted or expired
	ResetAt  string            `json:"reset_at,omitempty"`
	Bucket   string            `json:"bucket,omitempty"`
}

// KeyPolicy says a key's restrictions are read from these fields. A listing row without the models field permits
// nothing, which is the safe reading of an unreadable restriction.
type KeyPolicy struct {
	Models    string `json:"models"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// KeySecret reads one key's text.
type KeySecret struct {
	Path   string `json:"path"`
	Token  string `json:"token"`
	Bucket string `json:"bucket,omitempty"`
}

// KeyCreate makes a key. The answer must name the new key's id; a relay that answers only "ok" cannot be declared,
// because its text could not be read back.
type KeyCreate struct {
	Path   string            `json:"path"`
	Body   map[string]string `json:"body,omitempty"`
	Ref    string            `json:"ref"`
	Bucket string            `json:"bucket,omitempty"`
}

// Charges reads the per-request charge records. A declarative adapter requires it: without the records the cost of a
// call cannot be reconciled, and the supplier would silently be one whose cost is only estimated.
type Charges struct {
	Path     string            `json:"path"` // must hold {start}, {end}, {page} and {size}
	PageSize int               `json:"page_size"`
	Items    string            `json:"items"`
	Ref      string            `json:"ref"`
	Key      string            `json:"key"`
	Time     string            `json:"time"`
	Amount   string            `json:"amount"`
	Model    string            `json:"model,omitempty"`
	Funding  *Funding          `json:"funding,omitempty"`
	Usage    map[string]string `json:"usage,omitempty"` // catalog item name to a field path holding its count
	Filter   *Filter           `json:"filter,omitempty"`
	Bucket   string            `json:"bucket,omitempty"`
}

// Filter skips records whose field holds another value, such as a log that also lists top-ups.
type Filter struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

// Calls is the call half: which formats the relay speaks and how its refusals map to causes.
type Calls struct {
	Formats         []string `json:"formats"`
	Streams         bool     `json:"streams,omitempty"`
	Cancels         bool     `json:"cancels,omitempty"`
	ChargeRefHeader string   `json:"charge_ref_header"`
	Errors          ErrorMap `json:"errors"`
}

// ErrorMap reads the error code and optional reset time from a refusal, then applies the rules in order.
type ErrorMap struct {
	Code    string      `json:"code,omitempty"`
	ResetAt string      `json:"reset_at,omitempty"`
	Rules   []ErrorRule `json:"rules,omitempty"`
}

// ErrorRule turns a status and an optional code into a cause.
type ErrorRule struct {
	Status   int    `json:"status,omitempty"` // 0 matches any status
	Code     string `json:"code,omitempty"`   // empty matches any code
	Cause    string `json:"cause"`
	Scope    string `json:"scope"`
	Executed string `json:"executed,omitempty"` // "no" or "unknown"; a 5xx may never say "no"
}
