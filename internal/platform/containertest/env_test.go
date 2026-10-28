package containertest

import "testing"

func TestUntrackedIn(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"empty environment", nil, ""},
		{"absent", []string{"PATH=/bin"}, ""},
		{"present", []string{"TARGET=first"}, "first"},
		{"last wins", []string{"TARGET=first", "PATH=/bin", "TARGET=second"}, "second"},
		{"empty value", []string{"TARGET="}, ""},
		{"empty override", []string{"TARGET=first", "TARGET="}, ""},
		{"exact name", []string{"TARGET_EXTRA=wrong", "TARGET=right", "OTHER_TARGET=wrong", "TARGET"}, "right"},
		{"embedded equals", []string{"TARGET=first=second"}, "first=second"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := untrackedIn(c.env, "TARGET"); got != c.want {
				t.Fatalf("untrackedIn(%v) = %q, want %q", c.env, got, c.want)
			}
		})
	}
}

func TestUntracked(t *testing.T) {
	for _, value := range []string{"", "value", "first=second"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AXIOM_UNTRACKED_TEST", value)
			if got := Untracked("AXIOM_UNTRACKED_TEST"); got != value {
				t.Fatalf("Untracked = %q, want %q", got, value)
			}
		})
	}
}
