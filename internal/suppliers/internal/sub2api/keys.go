package sub2api

import (
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

type keyDTO struct {
	ID        number     `json:"id"`
	Name      string     `json:"name"`
	Key       string     `json:"key"`
	GroupID   number     `json:"group_id"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
	Reset5h   string     `json:"reset_5h_at"`
	Reset1d   string     `json:"reset_1d_at"`
	Reset7d   string     `json:"reset_7d_at"`
}

func keyState(k keyDTO) adapter.KeyState {
	out := adapter.KeyState{
		Ref: adapter.KeyRef(k.ID), GroupRef: string(k.GroupID), Name: k.Name, Status: keyStatus(k.Status),
		Policy: &adapter.KeyPolicy{ExpiresAt: k.ExpiresAt},
	}
	if out.Status == adapter.KeyQuotaExhausted {
		out.ResetAt = k.resetAt()
	}
	return out
}
