package containertest

import (
	"strings"
	"testing"
)

func TestSeaweedFSMasterReady(t *testing.T) {
	for _, check := range []struct {
		name   string
		status string
		ready  bool
	}{
		{name: "election pending", status: `{}`},
		{name: "follower", status: `{"Leader":"master:9333"}`},
		{name: "leader without address", status: `{"IsLeader":true}`},
		{name: "leader elected", status: `{"IsLeader":true,"Leader":"master:9333","MaxVolumeId":1}`, ready: true},
		{name: "truncated response", status: `{"IsLeader":true,"Leader":"master:9333"`},
	} {
		t.Run(check.name, func(t *testing.T) {
			if got := seaweedFSMasterReady(strings.NewReader(check.status)); got != check.ready {
				t.Errorf("master ready for %s = %t, want %t", check.status, got, check.ready)
			}
		})
	}
}
