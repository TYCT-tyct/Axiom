package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B1 to B3 of #948, the suppliers' groups, keys, sync history, reconciliation, queue and intake rule. Each answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) ConnectSupplierGroup(context.Context, adminapi.ConnectSupplierGroupRequestObject) (adminapi.ConnectSupplierGroupResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) IdentifySupplierKey(context.Context, adminapi.IdentifySupplierKeyRequestObject) (adminapi.IdentifySupplierKeyResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) ListSupplierSyncRuns(context.Context, adminapi.ListSupplierSyncRunsRequestObject) (adminapi.ListSupplierSyncRunsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) RequestSupplierSync(context.Context, adminapi.RequestSupplierSyncRequestObject) (adminapi.RequestSupplierSyncResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) GetSupplierReconciliation(context.Context, adminapi.GetSupplierReconciliationRequestObject) (adminapi.GetSupplierReconciliationResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) ListSupplierCredentials(context.Context, adminapi.ListSupplierCredentialsRequestObject) (adminapi.ListSupplierCredentialsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) AddSupplierKeys(context.Context, adminapi.AddSupplierKeysRequestObject) (adminapi.AddSupplierKeysResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) TestSupplierCredential(context.Context, adminapi.TestSupplierCredentialRequestObject) (adminapi.TestSupplierCredentialResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) TestSupplierCredentials(context.Context, adminapi.TestSupplierCredentialsRequestObject) (adminapi.TestSupplierCredentialsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) SetSupplierCredentialEnabled(context.Context, adminapi.SetSupplierCredentialEnabledRequestObject) (adminapi.SetSupplierCredentialEnabledResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) RemoveSupplierCredential(context.Context, adminapi.RemoveSupplierCredentialRequestObject) (adminapi.RemoveSupplierCredentialResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) RemoveInvalidSupplierCredentials(context.Context, adminapi.RemoveInvalidSupplierCredentialsRequestObject) (adminapi.RemoveInvalidSupplierCredentialsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) ListSupplierInbox(context.Context, adminapi.ListSupplierInboxRequestObject) (adminapi.ListSupplierInboxResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) GetSupplierInboxSummary(context.Context, adminapi.GetSupplierInboxSummaryRequestObject) (adminapi.GetSupplierInboxSummaryResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) IgnoreSupplierInboxItem(context.Context, adminapi.IgnoreSupplierInboxItemRequestObject) (adminapi.IgnoreSupplierInboxItemResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) SetSupplierIntakeRule(context.Context, adminapi.SetSupplierIntakeRuleRequestObject) (adminapi.SetSupplierIntakeRuleResponseObject, error) {
	return nil, errNotImplemented
}
