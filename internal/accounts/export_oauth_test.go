package accounts

// SetOAuthEndpointsForTesting points a provider's three endpoints at a fake, so a test never reaches the internet.
// Tests only: no production path changes a provider's endpoints.
func (s *Service) SetOAuthEndpointsForTesting(provider, authorize, token, userinfo string) {
	p := s.providers[provider]
	p.authorize, p.token, p.userinfo = authorize, token, userinfo
	s.providers[provider] = p
}
