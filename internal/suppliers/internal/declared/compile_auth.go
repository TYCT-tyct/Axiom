package declared

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"axiom/internal/suppliers/internal/adapter"
)

func (c *compiler) auth(spec Auth) (authPlan, error) {
	var a authPlan
	var err error
	if a.fields, err = c.loginFields(spec.Fields); err != nil {
		return a, err
	}
	for _, f := range a.fields {
		a.hasToken = a.hasToken || f == adapter.FieldAccessToken
	}
	if err := c.place(&a, spec); err != nil {
		return a, err
	}
	if spec.Login == nil {
		if err := tokenOnly(a.fields, a.hasToken); err != nil {
			return a, err
		}
		return a, nil
	}
	if !slices.Contains(a.fields, adapter.FieldEmail) || !slices.Contains(a.fields, adapter.FieldPassword) {
		return a, errors.New("auth.login: email and password must both be accepted")
	}
	if a.login, err = c.login(*spec.Login); err != nil {
		return a, err
	}
	return a, nil
}

// tokenOnly checks a declaration that cannot log in: its token must be typed in, optionally with its expiry.
func tokenOnly(fields []adapter.LoginField, hasToken bool) error {
	if !hasToken || len(fields) > 2 || len(fields) == 2 && !slices.Contains(fields, adapter.FieldAccessTokenExpiresAt) {
		return errors.New("auth: without a login the fields must be access_token, optionally with access_token_expires_at")
	}
	return nil
}

// place says where the token goes: a header with a prefix, or a query member.
func (*compiler) place(a *authPlan, spec Auth) error {
	switch {
	case spec.Header != "" && spec.Query != "":
		return errors.New("auth: declare header or query, not both")
	case spec.Header == "" && spec.Query == "":
		return errors.New("auth: declare where the token goes (header or query)")
	case spec.Header != "":
		if !headerName(spec.Header) || strings.ContainsAny(spec.Prefix, "\r\n") {
			return fmt.Errorf("auth: header %q is not a header name", spec.Header)
		}
		a.header, a.prefix = http.CanonicalHeaderKey(spec.Header), spec.Prefix
	default:
		if spec.Prefix != "" || !queryName(spec.Query) {
			return fmt.Errorf("auth: query %q is not a query name, or has a prefix", spec.Query)
		}
		a.query = spec.Query
	}
	return nil
}

// queryName accepts the characters a query member name can carry unescaped; a name with a space or a separator would
// change the shape of the request.
func queryName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("&=?#%", r) {
			return false
		}
	}
	return true
}

func (*compiler) loginFields(names []string) ([]adapter.LoginField, error) {
	if len(names) == 0 {
		return nil, errors.New("auth.fields: declare access_token, or email and password")
	}
	fields, seen := make([]adapter.LoginField, 0, len(names)), map[string]bool{}
	for _, name := range names {
		f, err := loginField(name)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("auth.fields: %q is listed twice", name)
		}
		seen[name] = true
		fields = append(fields, f)
	}
	if seen["email"] != seen["password"] {
		return nil, errors.New("auth.fields: email and password go together")
	}
	if seen["access_token_expires_at"] && !seen["access_token"] {
		return nil, errors.New("auth.fields: access_token_expires_at goes with access_token")
	}
	return fields, nil
}

func loginField(name string) (adapter.LoginField, error) {
	switch name {
	case "access_token":
		return adapter.FieldAccessToken, nil
	case "access_token_expires_at":
		return adapter.FieldAccessTokenExpiresAt, nil
	case "email":
		return adapter.FieldEmail, nil
	case "password":
		return adapter.FieldPassword, nil
	default:
		return "", fmt.Errorf("auth.fields: %q is not access_token, access_token_expires_at, email or password", name)
	}
}

func (c *compiler) login(spec Login) (*loginPlan, error) {
	l := &loginPlan{}
	var err error
	if l.path, err = c.pathTemplate("auth.login.path", spec.Path); err != nil {
		return nil, err
	}
	if l.bucket, err = c.bucket("auth.login", spec.Bucket); err != nil {
		return nil, err
	}
	if l.body, err = c.loginBody(spec.Body); err != nil {
		return nil, err
	}
	if l.token, err = c.path("auth.login.token", spec.Token); err != nil {
		return nil, err
	}
	if l.expiresAt, err = c.optionalPath("auth.login.expires_at", spec.ExpiresAt); err != nil {
		return nil, err
	}
	return l, nil
}

func (c *compiler) loginBody(spec map[string]string) (map[string]tmpl, error) {
	if len(spec) == 0 {
		return nil, errors.New("auth.login.body: declare the members the login takes")
	}
	body := make(map[string]tmpl, len(spec))
	email, password := false, false
	for member, text := range spec {
		if !memberName(member) {
			return nil, fmt.Errorf("auth.login.body: %q is not a member name", member)
		}
		t, err := c.template("auth.login.body."+member, text, "email", "password")
		if err != nil {
			return nil, err
		}
		for _, p := range t.parts {
			email = email || p.variable == "email"
			password = password || p.variable == "password"
		}
		body[member] = t
	}
	if !email || !password {
		return nil, errors.New("auth.login.body: email and password must both be sent")
	}
	return body, nil
}
