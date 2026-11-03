package protocol_test

import (
	"bytes"
	"io/fs"
	"testing"
	"testing/iotest"

	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/streamcheck"
)

// TestConvertStreamsBetweenNativeFormats converts every Messages sample for a Responses client and every Responses
// sample for a Messages client, whole and byte by byte, and compares the client's stream with its golden copy,
// to-responses.sse or to-messages.sse, which go test -update rewrites. The assembled Responses stream with reasoning
// and a function call, which official docs do not give whole, is converted for a Messages client too. Every client
// stream passes its format's stream check (README.md「跨格式的流」).
func TestConvertStreamsBetweenNativeFormats(t *testing.T) {
	t.Parallel()
	for _, c := range nativeStreamCases(t) {
		t.Run(c.name, func(t *testing.T) { checkNativeStream(t, c) })
	}
}

// nativeStream is one sample of a native upstream, converted for the other native format's client.
type nativeStream struct {
	name, golden string
	client, from protocol.Format
}

// nativeStreamCases is every Messages and Responses sample, for the other format's client.
func nativeStreamCases(t *testing.T) []nativeStream {
	t.Helper()
	var streams []nativeStream
	for _, up := range formats[:2] {
		client, ext := protocol.Responses, "responses"
		if up.f == protocol.Responses {
			client, ext = protocol.Messages, "messages"
		}
		dirs, err := fs.Glob(samples, up.dir+"/*")
		if err != nil || len(dirs) == 0 {
			t.Fatalf("no %s samples: %v", up.dir, err)
		}
		for _, dir := range dirs {
			streams = append(streams, nativeStream{dir + "/upstream.sse", dir + "/to-" + ext + ".sse", client, up.f})
		}
	}
	streams = append(streams, nativeStream{
		"assembled/responses-reasoning-tool.sse", "assembled/responses-reasoning-tool.to-messages.sse",
		protocol.Messages, protocol.Responses,
	})
	return streams
}

// checkNativeStream converts the sample whole and byte by byte, compares the two, checks the client's stream and its
// golden copy.
func checkNativeStream(t *testing.T, c nativeStream) {
	t.Helper()
	upstream := sample(t, c.name)
	got := relayTo(t, c.client, c.from, bytes.NewReader(upstream), protocol.UsageContext{},
		protocol.NewMemory(0), settled)
	bytewise := relayTo(t, c.client, c.from, iotest.OneByteReader(bytes.NewReader(upstream)),
		protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if got.err != nil || bytewise.err != nil || !bytes.Equal(bytewise.out, got.out) {
		t.Fatalf("byte by byte: %v; whole: %v; byte by byte:\n%s\nwhole:\n%s",
			bytewise.err, got.err, bytewise.out, got.out)
	}
	check := streamcheck.Messages
	if c.client == protocol.Responses {
		check = streamcheck.Responses
	}
	if err := check(bytes.NewReader(got.out)); err != nil {
		t.Error(err)
	}
	golden(t, c.golden, got.out)
}
