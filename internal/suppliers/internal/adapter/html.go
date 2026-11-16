package adapter

import "strings"

// IsHTML reports whether a body starts with a web page, as a firewall or anti-bot check may return.
func IsHTML(body []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 64)])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}
