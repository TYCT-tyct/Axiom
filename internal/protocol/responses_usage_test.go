package protocol_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

const responseUsage = `{"input_tokens":5,"output_tokens":11}`

func TestResponsesIgnoreTopLevelUsage(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.incomplete"} {
		for _, top := range []string{
			`{"prompt_tokens":900,"completion_tokens":700}`, `{"output_tokens":1}`,
			`{"input_tokens":"invalid"}`, "null",
		} {
			for _, topFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/top_first=%t", kind, top, topFirst), func(t *testing.T) {
					frame := responsesUsageLocations(kind, responseUsage, top, topFirst)
					checkResponsesNormalUsage(t, responsesFailureStream("null", frame))
				})
			}
		}
	}
}

func responsesUsageLocations(kind, nested, top string, topFirst bool) string {
	response := strings.Replace(responsesObject, `"usage":null`, `"usage":`+nested, 1)
	if kind == "response.incomplete" {
		response = strings.Replace(response, `"status":"in_progress"`, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1)
	}
	usage := `,"usage":` + top
	if topFirst {
		return responsesEvent(kind, 4, usage+response)
	}
	return responsesEvent(kind, 4, response+usage)
}

func checkResponsesNormalUsage(t *testing.T, stream string) {
	t.Helper()
	for _, client := range []protocol.Format{protocol.Responses, protocol.Chat} {
		streamed := relayTo(t, client, protocol.Responses, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
		if streamed.err != nil {
			t.Fatal(streamed.err)
		}
		checkResponsesUsageReport(t, streamed.r.Usage())
		whole := aggregate(t, client, protocol.Responses, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
		if whole.err != nil {
			t.Fatal(whole.err)
		}
		checkResponsesUsageReport(t, whole.r.Usage())
	}
}

func checkResponsesUsageReport(t *testing.T, u protocol.Usage) {
	t.Helper()
	if u.Invalid != nil {
		t.Fatalf("invalid usage: %v", u.Invalid)
	}
	report := u.Segments[0]
	if report.Usage.Count(catalog.ItemInput) != 5 || report.Usage.OutputTotal() != 11 {
		t.Errorf("usage %+v, want input 5 and output 11", report)
	}
	if !report.Reported.Has(catalog.ItemInput) || !report.Reported.Has(catalog.ItemOutput) {
		t.Errorf("final usage flags lost: %+v", report)
	}
}

func TestResponsesFailurePrefersResponseUsage(t *testing.T) {
	for _, kind := range []string{"response.failed", "error"} {
		for _, tc := range []struct {
			nested, top string
			invalid     bool
		}{
			{responseUsage, `{"input_tokens":"invalid"}`, false},
			{`{"input_tokens":"invalid"}`, responseUsage, true},
			{"null", responseUsage, false},
		} {
			for _, topFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/top_first=%t", kind, tc.nested, topFirst), func(t *testing.T) {
					stream := responsesUsageLocations(kind, tc.nested, tc.top, topFirst)
					checkResponsesFailureUsage(t, stream, tc.invalid)
				})
			}
		}
	}
}

func checkResponsesFailureUsage(t *testing.T, stream string, invalid bool) {
	t.Helper()
	got := relay(t, protocol.Responses, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if !errors.Is(got.err, protocol.ErrUpstream) {
		t.Fatalf("error %v", got.err)
	}
	u := got.r.Usage()
	if (u.Invalid != nil) != invalid {
		t.Fatalf("invalid usage %v, want invalid %t", u.Invalid, invalid)
	}
	if !invalid {
		checkResponsesUsageReport(t, u)
	}
}
