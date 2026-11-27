package adapter

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Type is a channel type as its adapter describes it (docs/domains/suppliers-direct.md「渠道类型」): what the admin's type
// picker shows and what a request to connect one is checked against. The registry is the only list of types, so no other
// layer copies one.
type Type struct {
	Kind Kind
	Mode Mode
	// Vendor names the maker and picks the icon: the catalog author's slug for a type that sells one maker's models, the
	// platform's own for a cloud.
	Vendor string
	// Serves is the catalog authors whose models the type sells at their authors' official prices. Empty is a type that
	// may sell any author's models (compatible, a relay): at prices of its own, so none is priced from the catalog.
	Serves []string
	// Name is the brand's own name, not translated.
	Name  string
	Group TypeGroup
	// Entries are the price lists the type may be connected at, the default first; none when a person gives the
	// address (and the settlement currency).
	Entries []PriceEntry
	// Families are the call families the type speaks; for one whose family a person chooses, the choices.
	Families []Family
	// Forms are the shapes its credential may take, the default first; Fields are asked whatever the form.
	Forms  []Form
	Fields []Field
	// Lists says a free, authenticated request lists the models a key serves: its check and the periodic re-list. A
	// type that cannot is never re-listed (a re-list must never cost money); its models come from the catalog and a key
	// is checked once, when it is added, with the smallest generation. Balance says a key's balance can be read.
	Lists, Balance bool
}

// Mode is how Axiom reaches a supplier of the type (SupplierMode in the contract).
type Mode string

// The modes.
const (
	ModeRelay  Mode = "relay"
	ModeDirect Mode = "direct"
	ModeCloud  Mode = "cloud"
)

// TypeGroup is where the picker lists a type.
type TypeGroup string

// The groups, in the picker's order.
const (
	GroupOfficial TypeGroup = "official"
	GroupDomestic TypeGroup = "domestic"
	GroupCloud    TypeGroup = "cloud"
	GroupRelay    TypeGroup = "relay"
)

// PriceEntry is a price list a type may be connected at: an address and the currency of the list that holds there,
// one value, since the same model's yuan and dollar prices are separate lists, not an exchange rate apart
// (docs/domains/catalog.md「目录基线」). A request picks an entry by its ID, never a currency; one origin may have two.
// Account names, in the vendor's words, the account whose keys belong at the entry, for a vendor whose account and
// not the address decides the price list (Alibaba Cloud's China and international sites); empty otherwise.
type PriceEntry struct{ ID, Origin, Currency, Account string }

// Form is one shape of a credential: the fields it is made of.
type Form struct {
	Name   string
	Fields []Field
}

// Field is one thing a person fills in. Its kind follows from its name (FieldKindOf), so a name means one thing
// across every type. Header is, for a text field, the request header its value is sent in.
type Field struct {
	Name     string
	Required bool
	Pattern  string
	Max      int
	Header   string
}

// FieldKind is what a field holds and where a request carries it.
type FieldKind string

// The kinds. Keys, Regions and Deployments are the request's own members; the others are its fields map.
const (
	FieldKeys        FieldKind = "keys"        // one key a line, each its own credential
	FieldSecret      FieldKind = "secret"      // a secret line
	FieldJSON        FieldKind = "json"        // a secret JSON document
	FieldText        FieldKind = "text"        // a setting that is not secret
	FieldRegions     FieldKind = "regions"     // regions, each its own credential
	FieldDeployments FieldKind = "deployments" // Azure's deployments
)

// fieldKinds is the vocabulary of field names the admin page has words for; a new name is added here first.
var fieldKinds = map[string]FieldKind{
	"keys": FieldKeys, "api_key": FieldSecret, "access_key_id": FieldSecret, "secret_access_key": FieldSecret,
	"service_account_json": FieldJSON, "organization": FieldText, "project": FieldText, "api_version": FieldText,
	"regions": FieldRegions, "deployments": FieldDeployments,
}

// FieldKindOf is the kind of the field name; false for a name outside the vocabulary.
func FieldKindOf(name string) (FieldKind, bool) { k, ok := fieldKinds[name]; return k, ok }

// Described is an adapter that describes its type. Every direct and cloud adapter does; a relay's that does not is
// described by its kind alone (Relay).
type Described interface {
	Type() Type
}

// Relay is the type of a relay adapter that does not describe itself.
func Relay(k Kind) Type { return Type{Kind: k, Mode: ModeRelay, Name: string(k), Group: GroupRelay} }

var (
	origin   = regexp.MustCompile(`^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	currency = regexp.MustCompile(`^[A-Z]{3}$`)
	entryID  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// Check refuses a description a request could not be checked against: what the registry asks of every type at startup.
func (t Type) Check() error {
	switch {
	case t.Name == "":
		return errors.New("has no name")
	case !slices.Contains([]TypeGroup{GroupOfficial, GroupDomestic, GroupCloud, GroupRelay}, t.Group):
		return fmt.Errorf("group %q is not one of the picker's", t.Group)
	case !slices.Contains([]Mode{ModeRelay, ModeDirect, ModeCloud}, t.Mode):
		return fmt.Errorf("mode %q is not one", t.Mode)
	case t.Mode != ModeRelay && len(t.Forms) == 0:
		return errors.New("takes no credential")
	}
	if err := t.checkEntries(); err != nil {
		return err
	}
	return t.checkForms()
}

// checkEntries checks the price entries and the families they are spoken in.
func (t Type) checkEntries() error {
	ids := map[string]bool{}
	for _, e := range t.Entries {
		if !entryID.MatchString(e.ID) || ids[e.ID] {
			return fmt.Errorf("price entry %q: an ID is lower-case letters, digits and dashes, once each", e.ID)
		}
		if !origin.MatchString(e.Origin) || !currency.MatchString(e.Currency) {
			return fmt.Errorf("price entry %s: %s %s is not a bare https origin and a currency code", e.ID, e.Origin, e.Currency)
		}
		ids[e.ID] = true
	}
	if len(t.Entries) > 0 && len(t.Families) == 0 {
		return errors.New("speaks no family")
	}
	for _, f := range t.Families {
		if !slices.Contains([]Family{FamilyOpenAI, FamilyAnthropic, FamilyGemini}, f) {
			return fmt.Errorf("family %q is not one", f)
		}
	}
	return nil
}

// checkForms checks the type's fields and each form's, which together name a field once, and that each form has a
// secret.
func (t Type) checkForms() error {
	seen := map[string]bool{}
	if err := t.checkFields(t.Fields, seen); err != nil {
		return err
	}
	for _, form := range t.Forms {
		if err := t.checkFields(form.Fields, maps.Clone(seen)); err != nil {
			return fmt.Errorf("form %s: %w", form.Name, err)
		}
		if !slices.ContainsFunc(form.Fields, func(f Field) bool { k, _ := FieldKindOf(f.Name); return k != FieldText }) {
			return fmt.Errorf("form %s has no secret", form.Name)
		}
	}
	return nil
}

func (t Type) checkFields(fields []Field, seen map[string]bool) error {
	for _, f := range fields {
		kind, ok := FieldKindOf(f.Name)
		switch {
		case !ok:
			return fmt.Errorf("field %q is not in the vocabulary", f.Name)
		case seen[f.Name]:
			return fmt.Errorf("field %q is named twice", f.Name)
		case kind == FieldRegions && t.Mode != ModeCloud, kind == FieldDeployments && t.Mode != ModeCloud:
			return fmt.Errorf("field %q is only a cloud's", f.Name)
		case f.Header != "" && kind != FieldText:
			return fmt.Errorf("field %q is sent in a header but is not a setting", f.Name)
		}
		if f.Pattern != "" {
			if _, err := regexp.Compile(f.Pattern); err != nil {
				return fmt.Errorf("field %q: %w", f.Name, err)
			}
		}
		seen[f.Name] = true
	}
	return nil
}

// Has says the type asks for a field of the kind in any form.
func (t Type) Has(kind FieldKind) bool {
	in := func(f Field) bool { k, _ := FieldKindOf(f.Name); return k == kind }
	return slices.ContainsFunc(t.Fields, in) || slices.ContainsFunc(t.Forms, func(f Form) bool {
		return slices.ContainsFunc(f.Fields, in)
	})
}

// Entry is the price entry with the ID, the default for an empty one; false for a type without entries.
func (t Type) Entry(id string) (PriceEntry, bool) {
	if len(t.Entries) == 0 {
		return PriceEntry{}, false
	}
	if id == "" {
		return t.Entries[0], true
	}
	i := slices.IndexFunc(t.Entries, func(e PriceEntry) bool { return e.ID == id })
	if i < 0 {
		return PriceEntry{}, false
	}
	return t.Entries[i], true
}

// EntryAt is the price entry a supplier stored with origin and currency was connected at.
func (t Type) EntryAt(origin, currency string) (PriceEntry, bool) {
	i := slices.IndexFunc(t.Entries, func(e PriceEntry) bool { return e.Origin == origin && e.Currency == currency })
	if i < 0 {
		return PriceEntry{}, false
	}
	return t.Entries[i], true
}

// FormOf is the form named, the default for an empty name.
func (t Type) FormOf(name string) (Form, bool) {
	if len(t.Forms) == 0 {
		return Form{}, false
	}
	if name == "" {
		return t.Forms[0], true
	}
	i := slices.IndexFunc(t.Forms, func(f Form) bool { return f.Name == name })
	if i < 0 {
		return Form{}, false
	}
	return t.Forms[i], true
}

// Settings are what is not secret about one credential (credentials.settings): the form it takes, when the type has more
// than one, and the values of its text fields, each checked against the type that stores it.
type Settings map[string]string

// FormKey is the member of Settings that names the credential's form.
const FormKey = "form"

// FieldError is a field filled in wrong: its name and what is wrong, never the value.
type FieldError struct{ Field, Reason string }

func (e *FieldError) Error() string { return "adapter: the field " + e.Field + " " + e.Reason }

// Read checks what a person filled in for the form named: every field the form and the type ask for, only those, the
// required ones present and each one matching its pattern. It returns the form, the secrets by name and the settings.
func (t Type) Read(form string, values map[string]string) (Form, map[string]string, Settings, error) {
	f, ok := t.FormOf(form)
	if !ok {
		return Form{}, nil, nil, &FieldError{Field: FormKey, Reason: "is not one of the type's"}
	}
	fields := append(slices.Clone(f.Fields), t.Fields...)
	secrets, settings := map[string]string{}, Settings{}
	for name := range values {
		if !slices.ContainsFunc(fields, func(x Field) bool { return x.Name == name }) {
			return Form{}, nil, nil, &FieldError{Field: name, Reason: "is not asked for"}
		}
	}
	for _, field := range fields {
		if err := field.read(values, secrets, settings); err != nil {
			return Form{}, nil, nil, err
		}
	}
	if len(t.Forms) > 1 {
		settings[FormKey] = f.Name
	}
	return f, secrets, settings, nil
}

// Valid reports whether stored settings are ones the type could have written: what Reload checks before a call uses them.
func (t Type) Valid(s Settings) bool {
	if len(t.Forms) == 0 { // a type without a form of its own, as a relay's, writes no settings
		return len(s) == 0
	}
	values := map[string]string{}
	for k, v := range s {
		if k != FormKey {
			values[k] = v
		}
	}
	f, ok := t.FormOf(s[FormKey])
	if !ok || (len(t.Forms) > 1) != (s[FormKey] != "") {
		return false
	}
	for _, field := range append(slices.Clone(f.Fields), t.Fields...) {
		if kind, _ := FieldKindOf(field.Name); kind != FieldText {
			continue
		}
		if field.Required && values[field.Name] == "" || values[field.Name] != "" && field.check(values[field.Name]) != nil {
			return false
		}
		delete(values, field.Name)
	}
	return len(values) == 0
}

// read checks the field's value among values and puts it in secrets or settings by its kind. Keys, regions and
// deployments are the request's own members, checked where they are read.
func (f Field) read(values, secrets map[string]string, settings Settings) error {
	kind, _ := FieldKindOf(f.Name)
	if kind == FieldKeys || kind == FieldRegions || kind == FieldDeployments {
		return nil
	}
	v := strings.TrimSpace(values[f.Name])
	if err := f.check(v); err != nil {
		return err
	}
	switch {
	case v == "":
	case kind == FieldText:
		settings[f.Name] = v
	default:
		secrets[f.Name] = v
	}
	return nil
}

func (f Field) check(v string) error {
	switch {
	case v == "" && f.Required:
		return &FieldError{Field: f.Name, Reason: "is required"}
	case v == "":
		return nil
	case f.Max > 0 && len(v) > f.Max:
		return &FieldError{Field: f.Name, Reason: fmt.Sprintf("is longer than %d characters", f.Max)}
	case f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(v):
		return &FieldError{Field: f.Name, Reason: "does not have the form the type asks for"}
	}
	return nil
}
