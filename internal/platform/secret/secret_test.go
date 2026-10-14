package secret_test

import (
	"bytes"
	"encoding/json"           //nolint:depguard // generated code encodes with v1: a Secret must redact under it too
	jsonv2 "encoding/json/v2" //nolint:depguard // jsonx is built on v2: a Secret must redact under it
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"axiom/internal/platform/secret"
)

const planted = "planted-credential-明文"

func secretShapes(s secret.Secret) []any {
	return []any{
		s, &s,
		struct{ Value secret.Secret }{s},
		&struct{ Value secret.Secret }{s},
		[]secret.Secret{s},
		map[string]secret.Secret{"value": s},
	}
}

func assertRedacted(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, planted) || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("output was not redacted: %s", output)
	}
}

func TestSecretOutputs(t *testing.T) {
	s := secret.New(planted)
	var zero secret.Secret
	if s.Reveal() != planted || zero.Reveal() != "" {
		t.Fatal("Reveal changed the plaintext")
	}
	for _, value := range []secret.Secret{s, zero} {
		assertRedacted(t, value.String())
		assertRedacted(t, value.GoString())
		text, err := value.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, string(text))
		for _, marshal := range []func(any) ([]byte, error){json.Marshal, func(v any) ([]byte, error) { return jsonv2.Marshal(v) }} {
			for _, shape := range secretShapes(value) {
				data, err := marshal(shape)
				if err != nil {
					t.Fatal(err)
				}
				assertRedacted(t, string(data))
			}
		}
	}
}

// fmt and slog's text handler print an unexported field without calling its methods, so no [REDACTED] marker appears
// there; only the closure keeps the plaintext out.
func TestSecretUnexportedField(t *testing.T) {
	shape := struct{ v secret.Secret }{secret.New(planted)}
	outputs := []string{}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
		outputs = append(outputs, fmt.Sprintf(format, shape))
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("credential", slog.Any("credential", shape))
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("credential", slog.Any("credential", shape))
	for _, output := range append(outputs, logs.String()) {
		if strings.Contains(output, planted) {
			t.Fatalf("unexported field exposed plaintext: %s", output)
		}
	}
}

func TestSecretFormatting(t *testing.T) {
	s := secret.New(planted)
	for _, shape := range secretShapes(s) {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%b", "%o", "%O", "%c", "%U", "%t", "%f", "%F", "%e", "%E", "%g", "%G", "%a", "%20.3s", "%+020v", "%#q"} {
			t.Run(format, func(t *testing.T) { assertRedacted(t, fmt.Sprintf(format, shape)) })
		}
		// fmt handles %T and %p itself, without calling Formatter.
		for _, format := range []string{"%T", "%p"} {
			if strings.Contains(fmt.Sprintf(format, shape), planted) {
				t.Fatal("fmt exposed plaintext")
			}
		}
	}
}

func TestSecretInvalidFormatting(t *testing.T) {
	for _, shape := range secretShapes(secret.New(planted)) {
		for _, format := range []string{"%", "%[2]v", "%*s", "%.*s", "%Z", "%w", "%p", "%#p", "%T"} {
			if strings.Contains(fmt.Sprintf(format, shape), planted) {
				t.Fatalf("format %q exposed plaintext", format)
			}
		}
	}
}

func TestSecretSlog(t *testing.T) {
	for _, handler := range []struct {
		name string
		new  func(*bytes.Buffer) slog.Handler
	}{
		{"json", func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) }},
		{"text", func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) }},
	} {
		t.Run(handler.name, func(t *testing.T) {
			for _, shape := range secretShapes(secret.New(planted)) {
				var output bytes.Buffer
				logger := slog.New(handler.new(&output))
				logger.Info("credential", slog.Any("credential", shape), slog.Group("nested", slog.Any("credential", shape)))
				assertRedacted(t, output.String())
			}
		})
	}
}
