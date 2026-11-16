package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // reusable in-place protocol readers
)

// decoderBuffer keeps the input reader's address stable across decoder resets.
type decoderBuffer struct{ bytes.Buffer }

func (b *decoderBuffer) reset(dec *jsontext.Decoder, data []byte, opts ...jsontext.Options) *jsontext.Decoder {
	b.Buffer = *bytes.NewBuffer(data)
	if dec == nil {
		return jsontext.NewDecoder(&b.Buffer, opts...)
	}
	dec.Reset(&b.Buffer, opts...) // Reset forgets the options it was made with.
	return dec
}

// valueIn keeps the last value valid for the lifetime of data, past subsequent reads.
func valueIn(dec *jsontext.Decoder, data, v []byte) []byte {
	to := int(dec.InputOffset())
	return data[to-len(v) : to]
}
