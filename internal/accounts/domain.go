package accounts

import (
	"errors"
	"strings"
)

// ErrEmailDomainNotAllowed is an address whose domain the site's allow and deny lists refuse.
var ErrEmailDomainNotAllowed = errors.New("accounts: email domain not allowed")

// ErrBadDomainPolicy is an allow or deny list that is not a list of domains.
var ErrBadDomainPolicy = errors.New("accounts: invalid email domain policy")

// domainPermitted reports whether emailDomain may register on site. The deny list wins; an allow list that is not
// empty is the whole set of domains that may. An entry covers its subdomains, so example.com covers a.example.com but
// not badexample.com. A site with neither list takes every domain the address syntax allows.
func domainPermitted(site Site, emailDomain string) bool {
	if len(site.RegistrationDomainAllow) == 0 && len(site.RegistrationDomainDeny) == 0 {
		return true
	}
	if !validDomain(emailDomain) {
		return false
	}
	for _, entry := range site.RegistrationDomainDeny {
		if domainMatches(emailDomain, entry) {
			return false
		}
	}
	if len(site.RegistrationDomainAllow) == 0 {
		return true
	}
	for _, entry := range site.RegistrationDomainAllow {
		if domainMatches(emailDomain, entry) {
			return true
		}
	}
	return false
}

// domainMatches is an exact domain or one of its subdomains.
func domainMatches(domain, entry string) bool {
	return domain == entry || strings.HasSuffix(domain, "."+entry)
}

// validDomain is a lower-case DNS name with at least two labels: what an allow or deny entry and a domain under a
// policy must be. The empty label checks also reject a leading or trailing dot.
func validDomain(domain string) bool {
	if len(domain) < 3 || len(domain) > 253 || strings.HasSuffix(domain, ".") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}

// cleanDomains lower-cases, trims and de-duplicates a policy list, refusing an entry that is not a domain and a list
// longer than the table allows.
func cleanDomains(entries []string) ([]string, error) {
	if len(entries) > 100 {
		return nil, ErrBadDomainPolicy
	}
	out := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if !validDomain(entry) {
			return nil, ErrBadDomainPolicy
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		out = append(out, entry)
	}
	return out, nil
}

// lowerDomains normalizes a list read from the table, where a row written before the admin API may hold any case.
func lowerDomains(entries []string) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = strings.ToLower(strings.TrimSpace(entry))
	}
	return out
}
