package protocol

import (
	"encoding/base64"
	"strings"

	"axiom/internal/platform/jsonx"
)

// Gemini binds its thinking to a function call with a thought signature, and refuses a later turn whose call comes back
// without it. Google's OpenAI-compatible endpoint carries it on the Chat tool call as extra_content.google.
// thought_signature; Messages and Responses have no member for it, so a call converted for their clients carries the
// signature in its ID, which every client hands back unchanged on the call and on its result, and a request converted
// to Chat puts it back where Google reads it (README.md「跨格式转换」).

// signatureMark separates a call's own ID from the signature it carries. Every character of the result is one
// Anthropic's tool ID pattern admits, so a Messages client gets the ID as is.
const signatureMark = "_axts_"

// withSignature is the ID a converted call is given: its own, or its own carrying the signature.
func withSignature(id, signature string) string {
	raw, err := base64.StdEncoding.DecodeString(signature)
	if signature == "" || err != nil || strings.Contains(id, signatureMark) {
		return id // a signature that is not Google's base64 is not carried: the call keeps its own ID
	}
	return id + signatureMark + base64.RawURLEncoding.EncodeToString(raw)
}

// splitSignature undoes withSignature: the call's own ID and its signature, or the ID and "" for one carrying none.
func splitSignature(id string) (own, signature string) {
	own, carried, ok := strings.Cut(id, signatureMark)
	if !ok {
		return id, ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(carried)
	if err != nil || len(raw) == 0 {
		return id, ""
	}
	return own, base64.StdEncoding.EncodeToString(raw)
}

// googleExtra is the extra_content a Chat tool call carries a signature in.
func googleExtra(signature string) jsonx.Raw {
	return jsonValue(map[string]any{"google": map[string]string{"thought_signature": signature}})
}

// unsignedID takes the carried signature out of a converted request's call or result ID, a JSON string, and returns
// the ID the upstream knows the call by with the signature; an ID that is not a string is left for the checks after.
func unsignedID(id jsonx.Raw) (jsonx.Raw, string) {
	s, err := requestString(id)
	if err != nil {
		return id, ""
	}
	own, signature := splitSignature(s)
	if signature == "" {
		return id, ""
	}
	return jsonValue(own), signature
}
