package webui

import "axiom/internal/platform/httpserver"

// encodings are the precompressed variants web/scripts/embed.ts writes next to a file, in the order the server
// prefers them when the client likes them equally: brotli compresses text smaller than gzip.
var encodings = []struct{ name, suffix string }{{"br", ".br"}, {"gzip", ".gz"}}

// negotiate picks the content coding for a response from the request's Accept-Encoding headers: the variant the
// client weights highest, the server's order breaking ties, and "" (the file as it is) when the client accepts none
// of them or refuses them with q=0. A coding the client names takes its own weight; a "*" gives one to the rest.
func negotiate(accept []string, variants map[string][]byte) string {
	weights := httpserver.EncodingWeights(accept)
	best, bestWeight := "", 0.0
	for _, enc := range encodings {
		if _, ok := variants[enc.name]; !ok {
			continue
		}
		weight, named := weights[enc.name]
		if !named {
			weight = weights["*"]
		}
		if weight > bestWeight {
			best, bestWeight = enc.name, weight
		}
	}
	return best
}
