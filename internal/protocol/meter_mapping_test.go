package protocol

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

func TestOpenAIBreakdownsReplaceTogether(t *testing.T) {
	for _, tc := range []struct {
		name, first, next            string
		input, read, write, imageOut int64
		invalid                      bool
	}{
		{"cache detail arrives later", `{"input_tokens":100}`, `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, 0, 80, 80, 0, false},
		{"image detail arrives later", `{"output_tokens":100}`, `{"output_tokens":100,"output_tokens_details":{"image_tokens":100}}`, 0, 0, 0, 100, false},
		{"cleared input leaves its whole snapshot", `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, `{"input_tokens":0}`, 0, 80, 80, 0, false},
		{"cleared output leaves its whole snapshot", `{"output_tokens":100,"output_tokens_details":{"image_tokens":100}}`, `{"output_tokens":0}`, 0, 0, 0, 100, false},
		{"physical input went down", `{"input_tokens":100}`, `{"input_tokens":99}`, 100, 0, 0, 0, true},
		{"physical output went down", `{"output_tokens":100}`, `{"output_tokens":99}`, 0, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m meter
			for _, body := range []string{tc.first, tc.next} {
				var u openAIUsage
				if err := jsonx.Unmarshal([]byte(body), &u); err != nil {
					t.Fatal(err)
				}
				r, err := u.items(nil)
				if err != nil {
					t.Fatal(err)
				}
				m.report(r, nil)
			}
			got := m.result()
			if (got.Invalid != nil) != tc.invalid {
				t.Fatalf("invalid = %v", got.Invalid)
			}
			if tc.invalid {
				return
			}
			u := got.Segments[0].Usage
			for it, n := range map[catalog.Item]int64{catalog.ItemInput: tc.input, catalog.ItemCacheRead: tc.read, catalog.ItemCacheWrite: tc.write, catalog.ItemImageOut: tc.imageOut} {
				if u.Count(it) != n {
					t.Errorf("%s = %d, want %d", it, u.Count(it), n)
				}
			}
			if tc.imageOut > 0 && u.OutputTotal() != 100 {
				t.Errorf("double counted output: %d", u.OutputTotal())
			}
			if tc.read > 0 && u.InputTotal() != 100 {
				t.Errorf("physical input changed: %d", u.InputTotal())
			}
		})
	}
}
