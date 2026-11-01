package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"axiom/internal/accounts"
	"axiom/internal/fileparse"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// gateParsing refuses, before the parser sends a document to a backend, what the admission would refuse after it: a
// key that is disabled, expired, off its address or over its rate is answered here rather than after a parse. The
// check reads the database once, as the admission does, and the admission still makes its own: this answer is old by
// the time the reservation is written, and only there is anything reserved.
func (h *Handler) gateParsing(r *http.Request, c *Call, sessionBody []byte, req protocol.Request) error {
	if h.Parser == nil || req.File == nil || !req.File.Parsed() || req.Documents == 0 {
		return nil
	}
	c.Session = h.Router.SessionKey(routing.AccountID(c.caller.AccountID()), &req, sessionBody, strings.TrimSpace(r.Header.Get("X-Session-Id")))
	c.rate = h.precheck(r.Context(), c.site.ID, callDimension, keyKey(c.caller), keyHex(c.caller),
		c, r.Header.Get("X-Claude-Code-Context-Compacted") != "")
	c.gated = true
	a, err := h.Accounts.Check(r.Context(), c.caller, accounts.AdmitRequest{Domain: c.host, ClientIP: c.ip})
	if err != nil {
		return err
	}
	return h.judgeRate(a, c, callDimension)
}

// parseFiles replaces the request's inline files with the text of the node's parser, before anything is reserved: the
// quote is then the body that is forwarded, and routing no longer needs an endpoint that accepts documents. It runs
// before compression, which is why restoreFile puts the plugin and its count back afterwards.
//
// A file the request carries an annotation for is not parsed again: the text the client sent back stands in for it, so
// a client that reuses a parsed file pays no second time (OpenRouter《PDF Inputs》, read 2026-11-01).
func (h *Handler) parseFiles(ctx context.Context, c *Call, body []byte, req protocol.Request) ([]byte, protocol.Request, error) {
	file := req.File
	if file == nil || !file.Parsed() {
		return body, req, nil
	}
	if h.Parser == nil {
		return nil, req, invalid(fmt.Sprintf("plugin %q is not supported: %s", "file-parser", protocol.NoFileParser))
	}
	if req.Documents == 0 {
		return body, req, nil // only references: a rewrite would copy the history's annotations for nothing
	}
	web := req.Web // a model suffix like :online is not a body member, so a re-inspection cannot see it
	ctx, cancel := context.WithTimeout(ctx, fileparse.Timeout)
	defer cancel()
	// The rewrite holds the body, the arrays it joins and the new body before the call's quote exists, and encodes every
	// text into its part: a byte of text escapes to six bytes, and a request writes at most MaxParsedBytes of it, in the
	// parts and again in the new body. Charge that bound before RewriteFiles allocates any of it, then keep only the new
	// body's growth, which the call holds until it ends. A body it cannot hold is refused here, allocating nothing.
	const rewriteCopies = 3 // the body, the spliced items and the new body; the decoded files fit in the body's size
	const escape = 6        // a byte of text, escaped in its part and again in the new body
	work := int64(rewriteCopies*len(body)) + 2*escape*int64(protocol.MaxParsedBytes)
	if !h.Memory.Charge(work) {
		return nil, req, ErrOverloaded
	}
	count := &parseCount{}
	newBody, files, err := protocol.RewriteFiles(req.Format, body, count.parse(ctx, h, c))
	if err != nil {
		h.Memory.Refund(work)
		return nil, req, parseRefusal(ctx, h.Logger, c, err)
	}
	if err := h.chargeGrowth(c, len(body), len(newBody), work); err != nil {
		return nil, req, err
	}
	parsed, refs, err := inspectParsed(req.Format, newBody)
	if err != nil {
		h.Logger.ErrorContext(ctx, "the parsed file body is invalid", "request_id", c.ID.String(), "err", err)
		return nil, req, errParseFailed
	}
	// Every file the rewrite did not replace must be one it may leave: a reference by id, which routing pins to one
	// provider, rather than content it could not parse.
	if parsed.Documents > int64(refs) {
		return nil, req, invalid("The file-parser plugin can only parse files sent inline in the message content.")
	}
	if len(files) == 0 {
		return body, req, nil // the plugin asked, but the request carries no file content to parse
	}
	parsed.File, parsed.FileParses = file, count.n
	parsed.Web = web
	c.files = annotations(files)
	return newBody, parsed, nil
}

// inspectParsed reads the rewritten body, and counts the file references it still carries.
func inspectParsed(f protocol.Format, body []byte) (protocol.Request, int, error) {
	parsed, err := protocol.Inspect(f, body)
	if err != nil {
		return parsed, 0, err
	}
	refs, err := protocol.FileReferences(f, body)
	return parsed, refs, err
}

// parseCount is one request's parsing: the files it has parsed so far, which bounds a runaway history, and the parser it
// calls. It is the backend RewriteFiles is given.
type parseCount struct{ n int64 }

// parse returns the parse function RewriteFiles calls for each file.
func (p *parseCount) parse(ctx context.Context, h *Handler, c *Call) func(protocol.File) (string, error) {
	return func(f protocol.File) (string, error) {
		if p.n == protocol.MaxFilesPerRequest {
			return "", protocol.ErrTooManyFiles
		}
		// The parse holds the base64 copy it sends (1.34 times the file, so two) and the backend's answer at its cap.
		// The gateway credits the memory back when the call is done, however it ends.
		held := 2*int64(len(f.Data)) + fileparse.ResponseLimit
		if !h.Memory.Charge(held) {
			return "", ErrOverloaded
		}
		c.hold(func() { h.Memory.Refund(held) })
		text, err := h.Parser.Parse(ctx, fileparse.Document{Name: f.Name, Data: f.Data})
		if err != nil {
			return "", err
		}
		p.n++
		return text, nil
	}
}

// chargeGrowth turns the rewrite's work set, charged before it ran, into what the call keeps: the new body's growth,
// which replaces the file bytes admission counted. The rest is given back now that the rewrite's copies are gone, so
// the growth stays charged across the exchange.
func (h *Handler) chargeGrowth(c *Call, before, after int, work int64) error {
	grow := max(0, int64(after)-int64(before))
	if grow > work {
		h.Memory.Refund(work)
		if !h.Memory.Charge(grow) {
			return ErrOverloaded
		}
	} else {
		h.Memory.Refund(work - grow)
	}
	if grow > 0 {
		c.hold(func() { h.Memory.Refund(grow) })
	}
	return nil
}

// annotations is the file annotations of a reply: the files this call parsed, not the ones its request replayed.
func annotations(files []protocol.ParsedFile) []protocol.FileAnnotation {
	out := make([]protocol.FileAnnotation, 0, len(files))
	for _, f := range files {
		if f.Parsed {
			out = append(out, protocol.FileAnnotation{Hash: f.Hash, Name: f.Name, Text: f.Text})
		}
	}
	return out
}

// parseRefusal classifies a parse failure. The backend's own error is logged, never sent: its text may quote the file.
func parseRefusal(ctx context.Context, logger *slog.Logger, c *Call, err error) *Refusal {
	switch {
	case errors.Is(err, ErrOverloaded):
		return ErrOverloaded
	case errors.Is(err, protocol.ErrFileTooLarge), errors.Is(err, protocol.ErrTooManyFiles),
		errors.Is(err, fileparse.ErrTooLarge):
		return errParseTooLarge
	case errors.Is(err, protocol.ErrInvalid), errors.Is(err, fileparse.ErrEmptyFile):
		return invalid("The file-parser plugin could not read a file in this request.")
	}
	logger.WarnContext(ctx, "the file parser backend failed", "request_id", c.ID.String(), "err", err)
	return errParseFailed
}

// errParseTooLarge is a file, or the text it becomes, past the parse caps: the request is well formed, its content too
// big (docs/domains/gateway.md「错误」).
var errParseTooLarge = &Refusal{
	Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
	Message: "A file is larger than the file parser's limit.",
}

// restoreFile puts back what compression removed: the plugin and its count are routing and billing settings, not body
// members (docs/domains/billing.md「预留与结算」).
func restoreFile(req *protocol.Request, file *protocol.FileParser, parses int64) {
	req.File, req.FileParses = file, parses
}
