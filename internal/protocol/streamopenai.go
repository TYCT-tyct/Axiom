package protocol

import "axiom/internal/platform/jsonx"

// openAIStream reads the accounting and error envelope shared by OpenAI streams.
type openAIStream struct {
	eventReader
	report           *openAIUsage
	reportErr        error
	failed, rewrites bool
}

func (s *openAIStream) upstreamError() error {
	s.failed = s.dec.PeekKind() != 'n'
	return errorObject(&s.reader, &s.eventReader, errorFields)
}

func (s *openAIStream) rewritten() error {
	s.rewrites = true
	return s.skip()
}

// usage takes the usage out of the chunk for the meter, leaving null in its place.
func (s *openAIStream) usage() error {
	if s.dec.PeekKind() == 'n' {
		return s.skip()
	}
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
	if err := s.edit(len(v), "null"); err != nil {
		return err
	}
	s.report, s.e.usage = &openAIUsage{}, true
	s.reportErr = jsonx.Unmarshal(v, s.report)
	return nil
}
