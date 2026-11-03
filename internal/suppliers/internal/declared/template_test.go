package declared

import (
	"net/url"
	"testing"
)

// TestTemplateEscapesPathAndQuery: a + in a query is a space to the server, and &, = or # can add members; a path
// segment has its own separators. The values must come back unchanged through the URL parser.
func TestTemplateEscapesPathAndQuery(t *testing.T) {
	t.Parallel()
	tm, err := parseTemplate("test", "/api/{ref}/models?group={group}&note={note}", "ref", "group", "note")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ref":   "a/b?c d",
		"group": "vip+fast&other=x=y /空",
		"note":  "a b+c=d",
	}
	text := tm.expand(values)
	u, err := url.Parse(text)
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	path, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/"+values["ref"]+"/models" {
		t.Errorf("path = %q, want the ref round-tripped", path)
	}
	query := u.Query()
	for name, want := range map[string]string{"group": values["group"], "note": values["note"]} {
		if got := query.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
