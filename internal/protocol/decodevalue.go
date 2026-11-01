package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the request tree is decoded token by token
	"errors"
	"fmt"
	"strconv"
	"unsafe"

	"axiom/internal/platform/jsonx"
)

// membersFast and arrayFast are the unbudgeted readers, kept apart so the fast path inlines as it did before the
// budget existed.
func membersFast(v []byte) (requestObject, error) {
	var o requestObject
	if err := jsonx.Unmarshal(v, &o); err != nil || o == nil {
		return nil, shapeError("expected a request object")
	}
	return o, nil
}

func arrayFast(v jsontext.Value) ([]jsontext.Value, error) {
	if isNull(v) {
		return nil, nil
	}
	var out []jsontext.Value
	if err := jsonx.Unmarshal(v, &out); err != nil {
		return nil, shapeError("expected an array")
	}
	return out, nil
}

func jsonValueFast(v any) jsontext.Value {
	data, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return data
}

// membersWithin decodes an object of JSON values as written, charging each map entry first. Values alias v.
func membersWithin(v []byte, b *budget) (requestObject, error) {
	if b.fast() {
		return membersFast(v)
	}
	if err := b.draw(subDecoderMemory); err != nil {
		return nil, err
	}
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(v)), b: b}
	o := requestObject{}
	err := d.object(func() error {
		value, err := d.value()
		if err != nil {
			return err
		}
		if err := b.draw(mapEntryMemory + int64(len(d.name))); err != nil {
			return err
		}
		o[string(d.name)] = value
		return nil
	})
	if errors.Is(err, ErrMemory) {
		return nil, err
	}
	if err != nil || d.dec.PeekKind() != 0 {
		return nil, shapeError("expected a request object")
	}
	return o, nil
}

// arrayWithin decodes an array of JSON values as written, charging the slice as it grows.
func arrayWithin(v jsontext.Value, b *budget) ([]jsontext.Value, error) {
	if b.fast() {
		return arrayFast(v)
	}
	if isNull(v) {
		return nil, nil
	}
	if err := b.draw(subDecoderMemory); err != nil {
		return nil, err
	}
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(v)), b: b}
	var out []jsontext.Value
	err := d.array(func() error {
		value, err := d.value()
		if err != nil {
			return err
		}
		if out, err = growWithin(out, b); err != nil {
			return err
		}
		out[len(out)-1] = value
		return nil
	})
	if errors.Is(err, ErrMemory) {
		return nil, err
	}
	if err != nil {
		return nil, shapeError("expected an array")
	}
	return out, nil
}

// jsonValueWithin is jsonValue drawing the encoded value's allocation from b first: the same value jsonValue writes,
// whose size is counted without encoding it.
func jsonValueWithin(v any, b *budget) jsontext.Value {
	if b.fast() {
		return jsonValueFast(v)
	}
	if err := b.draw(encodedSize(v) + structureMemory(v)); err != nil {
		return jsontext.Value("null") // the stage checks b.err before it returns
	}
	data, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return data
}

// encodedSize is how many bytes jsonx.MarshalCanonical writes for a value the conversions construct, counted without
// allocating it.
//
//nolint:cyclop,funlen,gocognit // one arm per kind the conversions construct; counting each kind in place keeps it exact
func encodedSize(v any) int64 {
	switch v := v.(type) {
	case nil:
		return 4
	case jsontext.Value:
		return int64(len(v))
	case string:
		return int64(quotedLength(v))
	case bool:
		if v {
			return 4
		}
		return 5
	case int:
		return int64(len(strconv.Itoa(v)))
	case int64:
		return int64(len(strconv.FormatInt(v, 10)))
	case requestObject:
		n := int64(2)
		first := true
		for name, value := range v {
			if !first {
				n++
			}
			first = false
			n += int64(quotedLength(name)) + 1 + encodedSize(value)
		}
		return n
	case []requestObject:
		return listSize(len(v), func(i int) int64 { return encodedSize(v[i]) })
	case []jsontext.Value:
		return listSize(len(v), func(i int) int64 { return encodedSize(v[i]) })
	case []any:
		return listSize(len(v), func(i int) int64 { return encodedSize(v[i]) })
	case map[string]any:
		n := int64(2)
		first := true
		for name, value := range v {
			if !first {
				n++
			}
			first = false
			n += int64(quotedLength(name)) + 1 + encodedSize(value)
		}
		return n
	case float64:
		return int64(len(strconv.FormatFloat(v, 'g', -1, 64)))
	}
	// A type the conversions do not build: encode it to learn its size, which is exact though it allocates first.
	data, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("protocol: invalid constructed JSON: %v", err))
	}
	return int64(len(data))
}

// structureMemory is what the maps and slices of a constructed value take beyond the bytes they encode, counted
// recursively: the maps were made while reading the request and are alive until the value is written.
func structureMemory(v any) int64 {
	switch v := v.(type) {
	case requestObject:
		n := mapEntryMemory * int64(len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	case []requestObject:
		n := sliceMemory[requestObject](len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	case []jsontext.Value:
		n := sliceMemory[jsontext.Value](len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	case []any:
		n := sliceMemory[any](len(v))
		for _, value := range v {
			n += structureMemory(value)
		}
		return n
	}
	return 0
}

func sliceMemory[T any](n int) int64 {
	return rounded(int64(n) * int64(unsafe.Sizeof(*new(T)))) //nolint:gosec // a Go type's size fits in int64
}

func listSize(n int, element func(int) int64) int64 {
	if n == 0 {
		return 2
	}
	size := int64(2 + n - 1)
	for i := range n {
		size += element(i)
	}
	return size
}

// unmarshalWithin decodes a small value of a request, drawing its allocation from b first.
func unmarshalWithin(v []byte, dst any, b *budget) error {
	if err := b.draw(int64(len(v)) + 512); err != nil {
		return err
	}
	return jsonx.Unmarshal(v, dst)
}
