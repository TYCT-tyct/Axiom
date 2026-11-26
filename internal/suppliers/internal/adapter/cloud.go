package adapter

import (
	"context"
	"net/http"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
)

// Cloud is the management half of a cloud platform's model service, reached with a credential a person pastes
// (docs/domains/suppliers.md「云平台」): Vertex AI's service account, Bedrock's access key, an Azure OpenAI resource's key.
// There is no login, balance or charge list to read: what can be asked is whether the credential serves in a region,
// and which models. The sync never opens such an adapter.
type Cloud interface {
	// Origin is the platform's address as the supplier shows it; empty for one whose address a person gives.
	Origin() string
	// Regional says a credential serves by region, each checked and limited on its own; a platform that does not takes
	// no region.
	Regional() bool
	// Named says a person names the models a credential serves, which the platform does not list to it (Azure
	// OpenAI's deployments); a check of such a platform lists none, and its Offer offers none.
	Named() bool
	// ReadCredential reads what a person pasted, asking nothing of the platform. A *CredentialFormError is one that
	// cannot be a credential of the platform.
	ReadCredential(in CloudInput) (CloudCredential, error)
	// CheckRegion asks the platform at origin, with requests it does not charge for, whether key serves in region
	// (empty for a platform without regions) and which models. at is the time the requests are signed at.
	CheckRegion(ctx context.Context, d Doer, origin, region string, key secret.Secret, at time.Time) CloudCheck
	// Offer is the name calls name the catalog model m by, for a platform whose check lists no models: official is the
	// name m's vendor's own API knows it by. false for a model the platform does not serve, and for every model of a
	// platform whose check lists them.
	Offer(m catalog.Model, official string) (string, bool)
	// Formats is what an endpoint of m serves at the platform, from the model's own abilities; none when it cannot.
	Formats(m catalog.Model) []protocol.Format
	// Unmapped is what a name a check lists that the catalog does not map yet is listed as speaking.
	Unmapped() []protocol.Format
}

// CloudInput is what a person pasted, by the contract's members (CloudCredentialInput). Only those of one platform are
// set; ReadCredential refuses the others.
type CloudInput struct {
	ServiceAccountJSON, AccessKeyID, SecretAccessKey, APIKey string
}

// CloudCredential is a credential as read: Key is what is sealed and what calls are made with; Identity tells this
// credential from another without being shown (it is hashed into the credential's ref); Last4 is all of it that is
// shown, beside what is not secret about it (Account).
type CloudCredential struct {
	Key      secret.Secret
	Identity string
	Last4    string
	Account  CloudAccount
}

// CloudAccount is what is not secret about a credential (CloudAccount in the contract).
type CloudAccount struct {
	ProjectID      string `json:"project_id,omitempty"`
	ClientEmail    string `json:"client_email,omitempty"`
	AccessKeyLast4 string `json:"access_key_last4,omitempty"`
	KeyLast4       string `json:"key_last4,omitempty"`
}

// CredentialFormError is a credential that cannot be one of the platform: Member is the contract's member at fault,
// Reason what is wrong, never any of the text pasted.
type CredentialFormError struct{ Member, Reason string }

func (e *CredentialFormError) Error() string {
	return "adapter: the credential's " + e.Member + " " + e.Reason
}

// CloudModel is one model a region serves. Name is what the endpoint is stored by and what is shown; Call is what this
// region's calls send in its place, empty when it is Name; Known is the name the catalog recognises the model by,
// empty when it is Name.
type CloudModel struct {
	Name, Call, Known string
}

// CloudCheck is the answer to one region's check. Status is the platform's HTTP status, 0 when it answered nothing.
// Models is nil for a platform whose check lists none (Offer gives its models).
type CloudCheck struct {
	Outcome KeyOutcome
	Cause   KeyCause
	Status  int
	Models  []CloudModel
}

// Doer is the outbound client. A cloud platform is reached at a host of each region's and at its token service, so its
// requests are not bound to one origin as Client's are; it declares no rate buckets for them to be counted in.
type Doer interface {
	Do(r *http.Request) (*http.Response, error)
}

// Signer is the call half of a cloud platform, which a path and a header do not reach (Wire): the address follows from
// the credential's region and the model, the body carries the platform's version in place of the model, and the
// authorization is a signature over the request or a token the credential is exchanged for. The module's shared call
// code asks it for the request, and for the answer in the call format's own framing.
type Signer interface {
	// Sign is the request call becomes. A failure is the call's before anything reached the platform: the body is not
	// one the platform takes, or the credential was refused or could not be exchanged.
	Sign(ctx context.Context, d Doer, call SignedCall) (*http.Request, *Failure)
	// Answer is a 2xx response in the call format's framing; most platforms answer in it already.
	Answer(resp *http.Response) *http.Response
}

// SignedCall is one call to a cloud platform: the call format and body the gateway prepared, the request headers that
// go with it, the endpoint's origin, the credential's region and key, the model as this region's calls name it, and
// the time it is signed at.
type SignedCall struct {
	Format                protocol.Format
	Origin, Region, Model string
	Body                  []byte
	Header                http.Header
	Key                   secret.Secret
	At                    time.Time
}
