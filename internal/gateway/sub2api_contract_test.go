package gateway

import (
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The fixture records Axiom's outbound requests against the in-memory sub2api contract.
// ParseMetadataUserID (backend/internal/service/metadata_userid.go:35) and GenerateSessionHash
// (gateway_service.go:901), sub2api a3298b46, require device_id and session_id and use the latter verbatim.
func TestRecordedSub2apiMetadataContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/sub2api/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var recording struct {
		Device  string `json:"device"`
		Session string `json:"session"`
		Turns   []struct {
			Client   jsonx.Raw `json:"client"`
			Upstream jsonx.Raw `json:"upstream"`
		} `json:"turns"`
	}
	if err := jsonx.Unmarshal(raw, &recording); err != nil {
		t.Fatal(err)
	}
	if len(recording.Turns) < 2 || recording.Device == "" || recording.Session == "" {
		t.Fatal("recording lacks two turns or the expected parsed identity")
	}
	for _, turn := range recording.Turns {
		call := runTurn(t, conversation{format: protocol.Messages}, clientTurn{body: string(turn.Client)})
		checkSub2apiRecording(t, call, turn.Upstream, parsedUserID{device: recording.Device, session: recording.Session})
	}
}

func checkSub2apiRecording(t *testing.T, call suppliers.Call, recorded jsonx.Raw, expected parsedUserID) {
	t.Helper()
	var got, want any
	if err := jsonx.Unmarshal(call.Body, &got); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal(recorded, &want); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("outbound request differs from recording (-want +got):\n%s", diff)
	}
	metadata := got.(map[string]any)["metadata"].(map[string]any)["user_id"].(string)
	id, ok := parseUserID(t, metadata)
	if !ok || id.device != expected.device || id.session != expected.session || sessionOf(t, call) != expected.session {
		t.Fatalf("sub2api could not pin the recorded request: %+v", id)
	}
}
