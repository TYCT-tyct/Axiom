package accounts

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestKeyUseSignalNeverWaits(t *testing.T) {
	at := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	s := &Service{keyUses: make(chan keyUse, 1), now: func() time.Time { return at }}
	s.observeKeyUse(Caller{})
	s.observeKeyUse(Caller{})
	if got := <-s.keyUses; !got.at.Equal(at) {
		t.Fatal(got)
	}
	if err := s.flushKeyUses(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.trackKeyUses(ctx, slog.New(slog.DiscardHandler))
}
