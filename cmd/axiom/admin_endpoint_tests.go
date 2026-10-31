package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// adminFormats are the call formats as the contract names them, in protocol's enumeration order.
var adminFormats = [...]adminapi.TestFormat{adminapi.TestFormat("messages"), adminapi.TestFormat("responses"), adminapi.TestFormat("chat")}

// formatOf is the call format a request names.
func formatOf(f adminapi.TestFormat) (protocol.Format, error) {
	for i, name := range adminFormats {
		if f == name {
			return protocol.Format(i), nil
		}
	}
	return 0, invalidField("/format", "must be one of messages, responses or chat")
}

// endpointBody is an endpoint as getSupplier lists it, for a person to pick one to test.
func endpointBody(e suppliers.Endpoint) adminapi.SupplierEndpoint {
	return adminapi.SupplierEndpoint{
		Id: e.ID.String(), GroupId: e.Group.String(), ModelId: e.Model.String(), UpstreamName: e.UpstreamName,
		Formats: modelFormats(e.Formats), CostState: adminapi.SupplierEndpointCostState(e.CostState), Enabled: e.Enabled,
	}
}

// modelFormats are the call formats as the store names them, which are the contract's names.
func modelFormats(formats []string) []adminapi.ModelFormat {
	out := make([]adminapi.ModelFormat, len(formats))
	for i, f := range formats {
		out[i] = adminapi.ModelFormat(f)
	}
	return out
}

// TestSupplierEndpoint runs one real generation against an endpoint of the node's call path, which the platform pays
// for: 10 a minute per session, as detectSupplier, because each one is a call to a third party.
func (a adminAPI) TestSupplierEndpoint(ctx context.Context, request adminapi.TestSupplierEndpointRequestObject) (adminapi.TestSupplierEndpointResponseObject, error) {
	if err := a.tests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	id, err := idIn("/endpoint_id", request.Body.EndpointId)
	if err != nil {
		return nil, err
	}
	format, err := formatOf(request.Body.Format)
	if err != nil {
		return nil, err
	}
	// Keep the claim and cost commit alive when the browser disconnects, with time to record a timed-out generation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	body, replayed, err := idempotently(ctx, a.db, "testSupplierEndpoint", request.Params.IdempotencyKey,
		[]any{id, string(request.Body.Format), request.Body.Reason},
		func(tx pg.Tx) (adminapi.EndpointTest, error) {
			// The generation is inside the claim, so a retry with the key replays the first answer instead of paying
			// for another, and the cost row commits with the answer the person reads. It is not cut short with the
			// request: the supplier bills the generation whole either way (suppliers.TestEndpoint).
			result, err := a.calls.TestEndpoint(ctx, suppliers.TestRequest{Endpoint: id, Format: format})
			if err != nil {
				return adminapi.EndpointTest{}, err
			}
			if err := suppliers.RecordTest(ctx, tx, actor(ctx), request.Body.Reason, result); err != nil {
				return adminapi.EndpointTest{}, err
			}
			return endpointTestBody(result), nil
		})
	if err != nil {
		return nil, err
	}
	return adminapi.TestSupplierEndpoint201JSONResponse{
		Body:    body,
		Headers: adminapi.TestSupplierEndpoint201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// endpointTestBody is a test's result as the contract answers it.
func endpointTestBody(r suppliers.TestResult) adminapi.EndpointTest {
	body := adminapi.EndpointTest{
		RequestId: uuid.UUID(r.Request).String(), EndpointId: r.Endpoint.String(), ModelId: r.Model.String(),
		UpstreamModel: r.Upstream, Format: adminFormats[r.Format], Outcome: adminapi.EndpointTestOutcome(r.Outcome().String()),
		UpstreamStatus: &r.Status, TotalMs: int(r.Total.Milliseconds()), EstimatedNative: r.Estimate(),
		NativeUnit: adminapi.EndpointTestNativeUnit(r.NativeUnit),
	}
	if r.UpstreamRequest != "" {
		body.UpstreamRequestId = &r.UpstreamRequest
	}
	if r.FirstToken != nil {
		ms := int(r.FirstToken.Milliseconds())
		body.FirstTokenMs = &ms
	}
	if cause, scope, ok := r.Problem(); ok {
		problem := &adminapi.EndpointTestProblem{Cause: adminapi.EndpointTestCause(cause)}
		if scope != "" {
			s := adminapi.EndpointTestProblemScope(scope)
			problem.Scope = &s
		}
		body.Problem = problem
	}
	return body
}
