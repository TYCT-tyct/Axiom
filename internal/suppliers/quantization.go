package suppliers

import (
	"errors"
	"slices"
)

// Quantization is the precision an endpoint serves a model at, named as OpenRouter names the levels (《Provider
// Routing》「Quantization Levels」, 2026-11-01). An admin records it on the endpoint; the supplier sync never does.
// The empty value is "not recorded", which is not a level: a request that names levels does not match it.
type Quantization string

// The levels OpenRouter documents.
const (
	QuantizationInt4    Quantization = "int4"
	QuantizationInt8    Quantization = "int8"
	QuantizationFP4     Quantization = "fp4"
	QuantizationMXFP4   Quantization = "mxfp4"
	QuantizationNVFP4   Quantization = "nvfp4"
	QuantizationFP6     Quantization = "fp6"
	QuantizationFP8     Quantization = "fp8"
	QuantizationMXFP8   Quantization = "mxfp8"
	QuantizationFP16    Quantization = "fp16"
	QuantizationBF16    Quantization = "bf16"
	QuantizationFP32    Quantization = "fp32"
	QuantizationUnknown Quantization = "unknown"
)

// Quantizations are the levels in the order OpenRouter lists them, so a page offers them in the documented order.
var Quantizations = [...]Quantization{
	QuantizationInt4, QuantizationInt8, QuantizationFP4, QuantizationMXFP4, QuantizationNVFP4, QuantizationFP6,
	QuantizationFP8, QuantizationMXFP8, QuantizationFP16, QuantizationBF16, QuantizationFP32, QuantizationUnknown,
}

// ErrQuantization is a level OpenRouter does not name.
var ErrQuantization = errors.New("suppliers: not a quantization level OpenRouter names")

// ParseQuantization reads a level's stored or requested name.
func ParseQuantization(name string) (Quantization, error) {
	q := Quantization(name)
	if slices.Contains(Quantizations[:], q) {
		return q, nil
	}
	return "", ErrQuantization
}
