package page

import (
	"testing"
	"time"
	"uuid"
)

func TestUpperBoundClampsToExclusiveWindow(t *testing.T) {
	t.Parallel()
	until := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	id := uuid.NewV7()
	for _, tc := range []struct {
		name  string
		after *Cursor
		want  Cursor
	}{
		{"first", nil, Cursor{At: until}},
		{"earlier", &Cursor{At: until.Add(-time.Second), ID: id}, Cursor{At: until.Add(-time.Second), ID: id}},
		{"equal", &Cursor{At: until, ID: id}, Cursor{At: until}},
		{"later", &Cursor{At: until.Add(time.Second), ID: id}, Cursor{At: until}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UpperBound(until, tc.after); got != tc.want {
				t.Fatalf("%+v, want %+v", got, tc.want)
			}
		})
	}
}
