package payments

import (
	"errors"
	"mime"
	"net/http"

	"axiom/internal/platform/egress"
)

type gatewayResponse struct {
	status int
	header http.Header
	body   []byte
}

// sendGateway never exposes network errors, which may contain a secret-bearing URL, or the response body.
func sendGateway(client *egress.Client, req *http.Request, provider, readFailure string) (gatewayResponse, error) {
	resp, err := client.Do(req)
	if err != nil {
		return gatewayResponse{}, errors.New(provider + ": gateway request failed")
	}
	defer resp.Body.Close() //nolint:errcheck // the bounded read determines the outcome
	body, err := egress.ReadBody(resp.Body, 1<<20)
	if err != nil {
		return gatewayResponse{}, errors.New(provider + ": " + readFailure)
	}
	return gatewayResponse{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

func callbackForm(cb Callback, provider string) (string, error) {
	media, _, err := mime.ParseMediaType(cb.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || cb.RawQuery != "" {
		return "", errors.New(provider + ": invalid callback form")
	}
	return string(cb.Body), nil
}
