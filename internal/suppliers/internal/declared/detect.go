package declared

import (
	"context"
	"fmt"
	"net/http"

	"axiom/internal/suppliers/internal/adapter"
)

// Detect implements adapter.Adapter. It reads one public answer and requires every marker the declaration names: a
// header, a version, a generation or the CAPTCHA flag. A non-2xx is "not this system", because a probe that cannot read
// the public answer cannot claim the address.
func (a *Adapter) Detect(ctx context.Context, c adapter.Client) (adapter.Detection, error) {
	resp, err := send(ctx, c, http.MethodGet, a.detect.path, nil, a.detect.bucket, nil)
	if err != nil {
		return adapter.Detection{}, fmt.Errorf("declared: detect: %w", err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	if a.detect.header != "" && resp.Header.Get(a.detect.header) == "" {
		return adapter.Detection{}, adapter.ErrNotRecognised
	}
	return a.detected(resp.Body)
}

// detected reads the fields the declaration names. A named field the answer lacks means the site is not one this
// declaration recognises: taking the absence for an empty version would claim the wrong system.
func (a *Adapter) detected(body []byte) (adapter.Detection, error) {
	var d adapter.Detection
	var err error
	if a.detect.version != nil {
		if d.Version, err = detectedText(body, *a.detect.version); err != nil {
			return d, err
		}
	}
	if a.detect.generation != nil {
		if d.Generation, err = detectedText(body, *a.detect.generation); err != nil {
			return d, err
		}
	}
	if a.detect.captcha != nil {
		if d.Captcha, err = detectedCaptcha(body, *a.detect.captcha); err != nil {
			return d, err
		}
	}
	return d, nil
}

func detectedCaptcha(body []byte, p path) (bool, error) {
	raw, ok := p.lookup(body)
	if !ok {
		return false, adapter.ErrNotRecognised
	}
	captcha, ok := readBool(raw)
	if !ok {
		return false, adapter.ErrNotRecognised
	}
	return captcha, nil
}

func detectedText(body []byte, p path) (string, error) {
	raw, ok := p.lookup(body)
	if !ok {
		return "", adapter.ErrNotRecognised
	}
	text, ok := readText(raw)
	if !ok {
		return "", adapter.ErrNotRecognised
	}
	return text, nil
}
