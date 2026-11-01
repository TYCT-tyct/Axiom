package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"axiom/internal/fileparse"
	"axiom/internal/protocol"
)

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
	ctx, cancel := context.WithTimeout(ctx, fileparse.Timeout)
	defer cancel()
	count := &parseCount{}
	newBody, files, err := protocol.RewriteFiles(req.Format, body, count.parse(ctx, h, c))
	if err != nil {
		return nil, req, parseRefusal(ctx, h.Logger, c, err)
	}
	if len(files) == 0 {
		return body, req, nil // the plugin asked, but the request carries no file content to parse
	}
	if err := h.chargeGrowth(c, len(body), len(newBody)); err != nil {
		return nil, req, err
	}
	parsed, err := protocol.Inspect(req.Format, newBody)
	if err != nil {
		h.Logger.ErrorContext(ctx, "the parsed file body is invalid", "request_id", c.ID.String(), "err", err)
		return nil, req, errParseFailed
	}
	if parsed.Documents != 0 {
		return nil, req, invalid("The file-parser plugin cannot parse a file outside the message content.")
	}
	parsed.File, parsed.FileParses = file, count.n
	c.files = annotations(files)
	return newBody, parsed, nil
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

// chargeGrowth charges the body's growth to the node's budget: the parsed text replaces the file after admission has
// counted the original body, so holding the new one is accounted for like the one it replaces.
func (h *Handler) chargeGrowth(c *Call, before, after int) error {
	if grow := int64(after) - int64(before); grow > 0 {
		if !h.Memory.Charge(grow) {
			return ErrOverloaded
		}
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
	case errors.Is(err, protocol.ErrInvalid), errors.Is(err, fileparse.ErrEmpty):
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
