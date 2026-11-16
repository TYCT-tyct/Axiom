package protocol

import (
	"fmt"
	"reflect"
	"testing"
)

// Each conversion must carry all accounting even when the event has no client content.
func TestConversionPreservesCounters(t *testing.T) {
	want := event{content: 11, searches: 2, codeExecutions: 3, fileSearches: 5, toolCalls: 7, toolChecks: 13, toolErrors: 17}
	counts := reflect.ValueOf(want.eventCounts)
	for i := range counts.NumField() {
		if field := counts.Field(i); field.Kind() == reflect.Int64 && field.Int() == 0 {
			t.Fatalf("counter %s needs a nonzero test value", counts.Type().Field(i).Name)
		}
	}
	want.shape.Add([]byte("hello world"))
	for _, target := range []Format{Chat, Messages, Responses} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			var got event
			var err error
			if target == Chat {
				got, err = (&converted{}).settle(want, heartbeat, nil)
			} else {
				s := &chatToNative{inner: counterUpstream{e: want}, client: target, composed: true}
				got, err = s.event(nil, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.eventCounts != want.eventCounts {
				t.Fatalf("conversion lost accounting: got %+v, want %+v", got, want)
			}
		})
	}
}

type counterUpstream struct {
	upstream
	e event
}

func (s counterUpstream) event([]byte, *meter) (event, error) {
	s.e.role = heartbeat
	return s.e, nil
}
