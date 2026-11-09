package alloctest

import (
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestChildEnvironment: the child gets each key alloctest sets once, whatever this process already had. A peak child
// keeps the rest of GODEBUG, makes every collection stop the world and stops goroutines only at calls; the other
// measurements leave it alone.
func TestChildEnvironment(t *testing.T) {
	t.Setenv(childEnv, "parent")
	t.Setenv(childModeEnv, "parent")
	t.Setenv("GODEBUG", "http2client=0")

	for _, c := range []struct {
		mode    string
		godebug string
	}{
		{allocMode, "http2client=0"},
		{peakMode, "http2client=0,gcstoptheworld=2,asyncpreemptoff=1"},
	} {
		checkChildEnvironment(t, c.mode, c.godebug)
	}
}

// checkChildEnvironment: the child's environment holds each key alloctest sets exactly once, with the value the mode
// needs, and keeps the rest of GODEBUG.
func checkChildEnvironment(t *testing.T, mode, godebug string) {
	t.Helper()
	want := map[string]string{
		childEnv:     "TestChildEnvironment",
		childModeEnv: mode,
		"GODEBUG":    godebug,
	}
	seen := map[string]int{}
	for _, entry := range childEnvironment("TestChildEnvironment", mode) {
		key, value, _ := strings.Cut(entry, "=")
		if expected, ok := want[key]; ok {
			if value != expected {
				t.Errorf("%s: %s=%q, want %q", mode, key, value, expected)
			}
			seen[key]++
		}
	}
	for key := range want {
		if seen[key] != 1 {
			t.Errorf("%s: %s appears %d times, want once", mode, key, seen[key])
		}
	}
}

// dropped is the array TestPeakReadingSkipsDroppedMemory drops while a goroutine still has its address, and address
// what the goroutine ends with, so that the loop over it is not compiled away.
var (
	dropped []byte
	address uintptr
)

// TestPeakReadingSkipsDroppedMemory: in a peak child, a reading does not count memory the program dropped, even while a
// running goroutine still has its address in a register. A goroutine drops a 4 MiB array, keeps only its address as an
// integer and loops over it, calling nothing for most of each round; only then does the test read the heap, so every
// reading comes after the drop. A reading that stopped the loop where it was would scan the goroutine's registers
// conservatively and keep the array.
func TestPeakReadingSkipsDroppedMemory(t *testing.T) {
	if os.Getenv(childEnv) != t.Name() || os.Getenv(childModeEnv) != peakMode {
		if out, err := spawn(t, peakMode); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	const size = 4 << 20
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	dropped = make([]byte, size)
	held := liveHeap()
	var gone, stop atomic.Bool
	var loops sync.WaitGroup
	loops.Go(func() {
		a := reflect.ValueOf(dropped).Pointer()
		dropped = nil
		gone.Store(true)
		for !stop.Load() {
			for i := range 1 << 22 {
				a ^= uintptr(i) & 1 // stays inside the array
			}
			runtime.Gosched() // a call, where a reading may stop this goroutine
		}
		address = a
	})
	for !gone.Load() {
		runtime.Gosched()
	}
	var peak uint64
	for range 16 {
		peak = max(peak, liveHeap())
	}
	stop.Store(true)
	loops.Wait()
	if peak+size/2 > held {
		t.Errorf("with the 4 MiB array dropped, a reading saw %d bytes live, against %d while it was held", peak, held)
	}
}
