package httpserver

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// compressMin is the body size from which a response is gzipped: below it the saving is smaller than the 20 bytes of
// framing and the CPU spent.
const compressMin = 1024

var gzipPool = sync.Pool{New: func() any {
	// BestSpeed: this runs on the call path, where a few percent of size is cheaper than the milliseconds.
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed) //nolint:errcheck // the level is a constant the package accepts
	return w
}}

// Compress gzips the JSON responses of next that are at least compressMin bytes, for clients that accept gzip. Only
// application/json (and +json) is compressed: any other type, above all the streams (text/event-stream, NDJSON) the
// model calls answer with, passes through untouched, so no stream waits for a compressor's buffer. A response that
// is flushed before it reached the minimum is a stream too and goes out as it is. Static files are not compressed
// here; they are precompressed (internal/platform/webui).
func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || !acceptsGzip(r.Header.Values("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		c := &compressor{ResponseWriter: w}
		defer c.finish()
		next.ServeHTTP(c, r)
	})
}

func acceptsGzip(accept []string) bool {
	weights := EncodingWeights(accept)
	weight, named := weights["gzip"]
	if !named {
		weight = weights["*"]
	}
	return weight > 0
}

// EncodingWeights reads the Accept-Encoding headers of a request into the weight (q) the client gives each content
// coding, lower-cased; "*" stands for the codings not named. A coding with a weight that cannot be read is left out,
// as if the client had not named it.
func EncodingWeights(accept []string) map[string]float64 {
	weights := map[string]float64{}
	for _, field := range accept {
		for item := range strings.SplitSeq(field, ",") {
			coding, params, _ := strings.Cut(item, ";")
			q := 1.0
			if _, value, ok := strings.Cut(strings.ToLower(params), "q="); ok {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil {
					continue
				}
				q = parsed
			}
			weights[strings.ToLower(strings.TrimSpace(coding))] = q
		}
	}
	return weights
}

type compressMode int

const (
	undecided compressMode = iota // the headers are held, and the body up to compressMin
	passing                       // sent as the handler writes it
	gzipping
)

// compressor holds a response until it knows whether to compress it. Invariant: in undecided nothing has reached
// the client; buffer is then all the handler wrote.
type compressor struct {
	http.ResponseWriter
	mode   compressMode
	status int
	buffer []byte
	gz     *gzip.Writer
}

func (c *compressor) WriteHeader(status int) {
	if status < http.StatusOK { // an interim response (1xx) is not the answer
		c.ResponseWriter.WriteHeader(status)
		return
	}
	if c.status == 0 {
		c.status = status
	}
}

func (c *compressor) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	switch c.mode {
	case passing:
		return c.ResponseWriter.Write(p)
	case gzipping:
		return c.gz.Write(p)
	case undecided:
	}
	if !c.eligible() {
		c.pass()
		return c.ResponseWriter.Write(p)
	}
	c.buffer = append(c.buffer, p...)
	if len(c.buffer) >= compressMin {
		c.compress()
	}
	return len(p), nil
}

// eligible says whether this response is one to compress, from the headers the handler set.
func (c *compressor) eligible() bool {
	h := c.Header()
	mediaType, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")) &&
		h.Get("Content-Encoding") == "" && h.Get("Content-Range") == "" && h.Get("ETag") == "" &&
		c.status != http.StatusNoContent && c.status != http.StatusNotModified
}

// pass sends what is held and passes every later write through.
func (c *compressor) pass() {
	c.mode = passing
	c.ResponseWriter.WriteHeader(c.status)
	if len(c.buffer) > 0 {
		_, _ = c.ResponseWriter.Write(c.buffer) //nolint:errcheck // the client went away; the next write reports it too
		c.buffer = nil
	}
}

func (c *compressor) compress() {
	c.mode = gzipping
	h := c.Header()
	h.Del("Content-Length") // the length of the uncompressed body
	h.Set("Content-Encoding", "gzip")
	c.ResponseWriter.WriteHeader(c.status)
	c.gz = gzipPool.Get().(*gzip.Writer) //nolint:forcetypeassert // the pool holds only gzip writers
	c.gz.Reset(c.ResponseWriter)
	_, _ = c.gz.Write(c.buffer) //nolint:errcheck // the client went away; the next write reports it too
	c.buffer = nil
}

// FlushError sends what the handler has written now: a handler that flushes is streaming, and its bytes do not wait
// for the minimum or for a compressor's buffer. Held bytes go out as they are; a body already being compressed is
// flushed through the compressor.
func (c *compressor) FlushError() error {
	switch c.mode {
	case undecided:
		if c.status == 0 {
			c.status = http.StatusOK
		}
		c.pass()
	case gzipping:
		if err := c.gz.Flush(); err != nil {
			return err
		}
	case passing:
	}
	return http.NewResponseController(c.ResponseWriter).Flush()
}

func (c *compressor) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// finish ends the response: held bytes below the minimum go out as they are, a compressed body is completed.
func (c *compressor) finish() {
	switch c.mode {
	case undecided:
		if c.status != 0 { // otherwise the handler wrote nothing, and net/http sends its empty 200
			c.pass()
		}
	case gzipping:
		_ = c.gz.Close() //nolint:errcheck // the client went away; there is no one to tell
		gzipPool.Put(c.gz)
	case passing:
	}
}
