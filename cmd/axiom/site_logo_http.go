package main

import (
	"bytes"
	"io"

	"axiom/internal/accounts"
)

// logoServedFor is the caching of a content-hash logo URL: the bytes never change under the hash.
const logoServedFor = "public, max-age=31536000, immutable"

// logoBody is the bytes at a logo URL and their length, for the generated response type of either surface.
func logoBody(logo *accounts.Logo) (io.Reader, int64) {
	return bytes.NewReader(logo.Bytes), int64(len(logo.Bytes))
}
