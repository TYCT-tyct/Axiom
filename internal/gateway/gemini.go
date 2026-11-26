package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"axiom/internal/protocol"
)

// The Gemini API's own paths, for the Gemini SDKs and Gemini CLI: generateContent, streamGenerateContent with alt=sse,
// and countTokens. A call runs as the Chat call protocol.GeminiToChat makes of it, routed, admitted and billed as any
// other, and its answer, refusals included, is rewritten into Gemini's at the edge (docs/domains/gateway.md「路径与格式」).

// geminiInbound is what a Gemini path says that its body does not: the model, whether to stream, and, once the body is
// read, what the reply must show.
type geminiInbound struct {
	model  string
	stream bool
	opts   protocol.GeminiOptions
}

// geminiMethods are the methods served, and whether each streams.
var geminiMethods = map[string]bool{"generateContent": false, "streamGenerateContent": true}

func (h *Handler) geminiRoutes() {
	h.mux.HandleFunc("POST /v1beta/models/{action...}", h.gemini)
}

// gemini serves POST /v1beta/models/{model}:{method}. The key may come as Google's clients send it, in x-goog-api-key
// or the key query parameter, as well as a bearer token.
func (h *Handler) gemini(w http.ResponseWriter, r *http.Request) {
	in := &geminiInbound{}
	gw := &geminiWriter{ResponseWriter: w, in: in, writeFor: h.Limits.cfg.WriteTimeout}
	defer gw.finish()
	geminiKey(r)
	action := r.PathValue("action")
	at := strings.LastIndexByte(action, ':')
	if at <= 0 {
		writeRefusal(gw, protocol.Chat, errNotFound)
		return
	}
	in.model, action = action[:at], action[at+1:]
	if action == "countTokens" {
		h.geminiCount(gw, r, in)
		return
	}
	stream, ok := geminiMethods[action]
	if !ok {
		writeRefusal(gw, protocol.Chat, errNotFound)
		return
	}
	if stream && r.URL.Query().Get("alt") != "sse" {
		writeRefusal(gw, protocol.Chat, invalid("streamGenerateContent is served as server-sent events: add alt=sse to the query."))
		return
	}
	in.stream = stream
	release, ok := h.ipSlot(gw, r, protocol.Chat)
	if !ok {
		return
	}
	defer release()
	c, err := h.acceptAs(gw, r, protocol.Chat, false, in)
	if err != nil {
		h.fail(gw, r, protocol.Chat, err)
		return
	}
	h.Run(gw, r, c)
}

// geminiKey moves a key given Google's way into the bearer header the call path reads, and takes it out of the query.
func geminiKey(r *http.Request) {
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
		return
	}
	key := r.Header.Get("X-Goog-Api-Key")
	if q := r.URL.Query(); key == "" && q.Has("key") {
		key = q.Get("key")
		q.Del("key")
		r.URL.RawQuery = q.Encode()
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
}

// geminiBody is the Chat request a Gemini call's body converts to, its memory held with the call's.
func (h *Handler) geminiBody(c *Call, body []byte) ([]byte, error) {
	in := c.gemini
	chat, opts, err := protocol.GeminiToChat(body, in.model, in.stream)
	if err != nil {
		return nil, invalid(strings.TrimPrefix(err.Error(), protocol.ErrInvalid.Error()+": "))
	}
	if !h.Memory.Charge(int64(cap(chat))) {
		return nil, ErrOverloaded
	}
	c.hold(func() { h.Memory.Refund(int64(cap(chat))) })
	in.opts = opts
	return chat, nil
}

// geminiCount answers countTokens: {"totalTokens": n}, counted as count_tokens counts.
func (h *Handler) geminiCount(w http.ResponseWriter, r *http.Request, in *geminiInbound) {
	release, ok := h.ipSlot(w, r, protocol.Chat)
	if !ok {
		return
	}
	defer release()
	n, err := h.countAs(w, r, protocol.Chat, in)
	if err != nil {
		h.fail(w, r, protocol.Chat, err)
		return
	}
	if gw, ok := w.(*geminiWriter); ok {
		w = gw.ResponseWriter // the count is Gemini's already
	}
	writeJSON(w, map[string]any{"totalTokens": n})
}

// geminiWriter rewrites the Chat answer the call path writes into Gemini's: an event stream event by event, and a
// whole body, a refusal's included, once the handler returns. Only a Gemini path pays for the second reading.
type geminiWriter struct {
	http.ResponseWriter
	in       *geminiInbound
	reply    *protocol.GeminiReply
	writeFor time.Duration
	status   int
	stream   bool
	pending  []byte // a stream's bytes short of a whole event, or the whole body
}

// Unwrap lets the call path's ResponseController reach the connection's deadlines.
func (g *geminiWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *geminiWriter) WriteHeader(status int) {
	if g.status != 0 {
		return
	}
	g.status = status
	g.stream = strings.HasPrefix(g.Header().Get("Content-Type"), "text/event-stream")
	g.Header().Del("Content-Length")
	if g.stream {
		g.ResponseWriter.WriteHeader(status)
	}
}

func (g *geminiWriter) Write(p []byte) (int, error) {
	if g.status == 0 {
		g.WriteHeader(http.StatusOK)
	}
	g.pending = append(g.pending, p...)
	if !g.stream {
		return len(p), nil
	}
	return len(p), g.drain()
}

// FlushError flushes what a stream has converted; a whole body waits for finish.
func (g *geminiWriter) FlushError() error {
	if !g.stream {
		return nil
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *geminiWriter) replyOf() *protocol.GeminiReply {
	if g.reply == nil {
		g.reply = protocol.NewGeminiReply(g.in.opts)
	}
	return g.reply
}

// drain converts the whole events received: a comment, the keep-alive, goes as it is; data becomes Gemini's.
func (g *geminiWriter) drain() error {
	for {
		end := bytes.Index(g.pending, []byte("\n\n"))
		if end < 0 {
			return nil
		}
		event := g.pending[:end]
		g.pending = g.pending[end+2:]
		var data []byte
		for line := range bytes.SplitSeq(event, []byte("\n")) {
			switch {
			case bytes.HasPrefix(line, []byte(":")):
				if _, err := g.ResponseWriter.Write(append(bytes.Clone(line), "\n\n"...)); err != nil {
					return err
				}
			case bytes.HasPrefix(line, []byte("data:")):
				data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
			}
		}
		if data == nil {
			continue
		}
		out, err := g.replyOf().Event(data)
		if err != nil {
			return err
		}
		if out != nil {
			if _, err := g.ResponseWriter.Write(append(append([]byte("data: "), out...), "\n\n"...)); err != nil {
				return err
			}
		}
	}
}

// finish writes a whole body in Gemini's shape, now that the call path has written all of it.
func (g *geminiWriter) finish() {
	if g.stream || g.status == 0 {
		return
	}
	body, err := g.replyOf().Body(g.status, g.pending)
	if err != nil {
		body = protocol.GeminiErrorBody(g.status, g.pending)
	}
	_ = http.NewResponseController(g.ResponseWriter).SetWriteDeadline(time.Now().Add(g.writeFor)) //nolint:errcheck // a writer without deadlines is written to as it is
	g.Header().Set("Content-Type", "application/json")
	g.ResponseWriter.WriteHeader(g.status)
	g.ResponseWriter.Write(body) //nolint:errcheck,gosec // the client may be gone; nothing more can be told
}
