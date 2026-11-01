package protocol

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"axiom/internal/platform/alloctest"
)

// TestDecodeChargesWhatItAllocates: the decode of the shapes that cost the most asks the budget for what it takes
// before it takes it, and a budget one byte short of the peak refuses. The peak is what the charges say; the total a
// child process measures, garbage included, is an upper bound on it.
func TestDecodeChargesWhatItAllocates(t *testing.T) {
	pair := `{"role":"user","content":"a"},{"role":"assistant","content":"b"}`
	nested := strings.Repeat("[", 200) + strings.Repeat("]", 200)
	cases := []struct {
		name string
		body []byte
	}{
		{"empty objects", []byte(`{"model":"m","messages":[` + strings.Repeat("{},", 20000) + `{}]}`)},
		{"null messages", []byte(`{"model":"m","messages":[` + strings.Repeat("null,", 20000) + `null]}`)},
		{"null arrays", []byte(`{"model":"m","messages":[{"role":"user","content":null,"tool_calls":null}]}`)},
		{"deep nesting", []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x","deep":` + nested + `}]}]}`)},
		{"small messages", []byte(`{"model":"m","messages":[` + strings.Repeat(pair+",", 20000) + pair + `]}`)},
		{"long text", []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"}]}`)},
		{"escapes", []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat(`\u0041`, 1<<18) + `"}]}`)},
	}
	funcs := make([]alloctest.Function, 0, len(cases))
	for _, c := range cases {
		body := c.body
		funcs = append(funcs, alloctest.Function{Name: c.name, F: func() { _, _ = decodeCanon(body) }})
	}
	measured := alloctest.Measure(t, funcs...)
	for _, c := range cases {
		b := &budget{m: NewMemory(1 << 40)}
		if _, err := decodeCanonWithin(c.body, b); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if b.peak == 0 {
			t.Errorf("%s: drew nothing", c.name)
		}
		if b.peak > int64(measured[c.name].Bytes)+16<<10 {
			t.Errorf("%s: drew %d bytes, more than the %.0f it allocated", c.name, b.peak, measured[c.name].Bytes)
		}
		short := &budget{m: NewMemory(b.peak - 1)}
		if _, err := decodeCanonWithin(c.body, short); !errors.Is(err, ErrMemory) {
			t.Errorf("%s: a byte short of %d did not refuse: %v", c.name, b.peak, err)
		}
	}
}

// TestPrepareLargeRequestReservesAboutItsBody: a cross-format conversion of a large request draws a few times the
// request's own bytes, not a bound that grows with its shape, and a byte short refuses.
func TestPrepareLargeRequestReservesAboutItsBody(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	for _, c := range []struct {
		name string
		from Format
		body []byte
		to   Format
	}{
		{"messages to chat", Messages, []byte(`{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"` + long + `"}]}`), Chat},
		{"messages to responses", Messages, []byte(`{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"` + long + `"}]}`), Responses},
		{"chat to messages", Chat, []byte(`{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"` + long + `"}]}`), Messages},
		{"responses to messages", Responses, []byte(`{"model":"m","max_output_tokens":1024,"input":[{"role":"user","content":[{"type":"input_text","text":"` + long + `"}]}]}`), Messages},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := Inspect(c.from, c.body)
			if err != nil {
				t.Fatal(err)
			}
			need := prepareNeed(t, &r, c.body, Target{Format: c.to, Model: "up", MaxOutput: 4096})
			if limit := 5 * int64(len(c.body)); need > limit {
				t.Errorf("drew %d bytes for a %d-byte body, more than %d", need, len(c.body), limit)
			}
			if _, _, err := PrepareBudgeted(NewMemory(need-1), &r, c.body, Target{Format: c.to, Model: "up", MaxOutput: 4096}, Rewrites{}); !errors.Is(err, ErrMemory) {
				t.Errorf("a byte short of %d did not refuse: %v", need, err)
			}
		})
	}
}

// TestPrepareConcurrentLargeRequests: requests that prepare at once each draw their peak from one budget before they
// allocate, and what they keep when done is their output's capacity; the failures take nothing and the budget is whole
// once the kept bodies are given back.
func TestPrepareConcurrentLargeRequests(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	body := []byte(`{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"` + long + `"}]}`)
	r, err := Inspect(Messages, body)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Format: Chat, Model: "up", MaxOutput: 4096}
	need := prepareNeed(t, &r, body, target)
	m := NewMemory(need)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var kept, failed int64
	for range 8 {
		wg.Go(func() {
			<-start
			u, held, err := PrepareBudgeted(m, &r, body, target, Rewrites{})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				if held != int64(cap(u.Body)) {
					t.Errorf("held %d, want the body's capacity %d", held, cap(u.Body))
				}
				kept += held
			case errors.Is(err, ErrMemory):
				failed++
			default:
				t.Errorf("prepare: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if kept == 0 || failed == 0 {
		t.Fatalf("%d prepared, %d refused; want both", kept/(1<<20), failed)
	}
	if kept > need {
		t.Errorf("kept %d bytes of a %d-byte budget", kept, need)
	}
	if left := need - kept; m.left.Load() != left {
		t.Errorf("%d bytes left, want %d", m.left.Load(), left)
	}
	m.Refund(kept) // the kept bodies are given back
	if m.left.Load() != need {
		t.Errorf("%d bytes left after giving back the bodies, want %d", m.left.Load(), need)
	}
}

// TestBudgetedDecodeMatchesTheFastPath: with a budget, the decoder must build the same tree jsonx builds for a
// Prepare outside a node. The conversions' goldens run through the fast path; this holds the budgeted path to it.
func TestBudgetedDecodeMatchesTheFastPath(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a\nb"},{"type":"image_url","image_url":{"url":"https://x/y.png","detail":"high"}}]}]}`,
		`{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`,
		`{"model":"m","functions":[{"name":"f","description":"d","parameters":{"type":"object"}}],"function_call":{"name":"f"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"assistant","name":"n","reasoning_details":[{"type":"reasoning.text","text":"a","index":0},{"type":"reasoning.text","text":"b","index":0}]}]}`,
		`{"model":"m","messages":[{"role":"assistant","content":"x","refusal":"no"}],"unknown":{"nested":[1,2,3]}}`,
		`{"model":"m","messages":[{"role":"user","content":null,"tool_calls":null}]}`,
	} {
		fast, err := decodeCanon([]byte(body))
		if err != nil {
			t.Fatalf("fast: %v", err)
		}
		slow, err := decodeCanonWithin([]byte(body), &budget{m: NewMemory(1 << 30)})
		if err != nil {
			t.Fatalf("budgeted: %v", err)
		}
		if !reflect.DeepEqual(fast, slow) {
			t.Errorf("the budgeted tree differs for %s", body)
		}
	}
}

// prepareNeed is the smallest budget PrepareBudgeted finishes with, found by asking.
func prepareNeed(t *testing.T, r *Request, body []byte, target Target) int64 {
	t.Helper()
	lo, hi := int64(0), int64(1)<<40
	for lo < hi {
		mid := lo + (hi-lo)/2
		if _, _, err := PrepareBudgeted(NewMemory(mid), r, body, target, Rewrites{}); errors.Is(err, ErrMemory) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
