package protocol

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestPrepareLargeRequestReservesAboutItsBody: a conversion from Chat of a large request draws a few times the
// request's own bytes, not a bound that grows with its shape, and a byte short refuses. The readers of the other two
// formats still reserve nativeRequestCost (README.md「跨格式转换」).
func TestPrepareLargeRequestReservesAboutItsBody(t *testing.T) {
	long := strings.Repeat("x", 1<<18)
	for _, c := range []struct {
		name string
		from Format
		body []byte
		to   Format
	}{
		{"chat to messages", Chat, []byte(`{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"` + long + `"}]}`), Messages},
		{"chat to responses", Chat, []byte(`{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"` + long + `"}]}`), Responses},
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
	long := strings.Repeat("x", 1<<18)
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
		t.Fatalf("%d bytes kept, %d refused; want both", kept, failed)
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

// TestMemoryErrorKeepsItsType: a budget exhausted inside a legacy function-call upgrade or a thinking budget is a
// memory error (503), not a shape error (400).
func TestMemoryErrorKeepsItsType(t *testing.T) {
	cases := []struct {
		name string
		from Format
		body []byte
		to   Format
	}{
		{"legacy function call", Chat, []byte(`{"model":"m","messages":[{"role":"assistant","function_call":{"name":"f"}}]}`), Chat},
		{"legacy functions", Chat, []byte(`{"model":"m","functions":[{"name":"f","parameters":{"type":"object"}}],"messages":[{"role":"user","content":"x"}]}`), Chat},
		{"thinking budget", Messages, []byte(`{"model":"m","max_tokens":2048,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","budget_tokens":1024}}`), Chat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Inspect(c.from, c.body)
			if err != nil {
				t.Fatal(err)
			}
			target := Target{Format: c.to, Model: "up", MaxOutput: 4096}
			need := prepareNeed(t, &r, c.body, target)
			_, _, err = PrepareBudgeted(NewMemory(need-1), &r, c.body, target, Rewrites{})
			if !errors.Is(err, ErrMemory) {
				t.Fatalf("a byte short of %d: %v, want ErrMemory", need, err)
			}
		})
	}
}

// TestPrepareRefusesTheOutputCleanly: a budget that holds the tree but not the output buffer refuses with ErrMemory,
// whatever the writer would have written first (here the system blocks, which look at what was written), and gives
// back everything it drew.
func TestPrepareRefusesTheOutputCleanly(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"` + strings.Repeat("x", 1<<18) + `"}]}`)
	r, err := Inspect(Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []Format{Messages, Responses} {
		target := Target{Format: to, Model: "up", MaxOutput: 4096}
		tree := &budget{m: NewMemory(1 << 40)}
		if _, err := decodeCanonWithin(body, tree); err != nil {
			t.Fatal(err)
		}
		left := tree.peak + 1024 // the tree fits, a buffer of the body's size does not
		m := NewMemory(left)
		if _, _, err := PrepareBudgeted(m, &r, body, target, Rewrites{}); !errors.Is(err, ErrMemory) {
			t.Errorf("format %v: %v, want ErrMemory", to, err)
		}
		if m.left.Load() != left {
			t.Errorf("format %v: %d of %d left after the refusal", to, m.left.Load(), left)
		}
	}
}

// TestReplayRefusesWithErrMemory: replay's own unquoting draws from the budget, and a refusal there stays ErrMemory,
// not the invalid replay a client is told of.
func TestReplayRefusesWithErrMemory(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"},{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"t","signature":"` + strings.Repeat(string(rune(0x5c))+"u0061", 1<<16) + `"}]}]}`)
	b := &budget{m: NewMemory(rounded(int64(len(body))) + 1024)} // the output fits, the signature's unquoting does not
	if _, err := replayBody(Messages, body, false, b); !errors.Is(err, ErrMemory) {
		t.Fatalf("%v, want ErrMemory", err)
	}
}
