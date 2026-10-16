package platform

import "testing"

func TestAttemptSeqIsOneToThree(t *testing.T) {
	t.Parallel()
	for seq, want := range map[AttemptSeq]bool{0: false, 1: true, 2: true, 3: true, 4: false, 255: false} {
		if seq.Valid() != want {
			t.Errorf("AttemptSeq(%d).Valid() = %v, want %v", seq, !want, want)
		}
	}
}
