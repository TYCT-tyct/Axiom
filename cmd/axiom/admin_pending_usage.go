package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B9 of #948, plugin use, every account's subscriptions and the announcements. Each answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) GetPluginStats(context.Context, adminapi.GetPluginStatsRequestObject) (adminapi.GetPluginStatsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) ListPlatformSubscriptions(context.Context, adminapi.ListPlatformSubscriptionsRequestObject) (adminapi.ListPlatformSubscriptionsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) GetSubscriptionSummary(context.Context, adminapi.GetSubscriptionSummaryRequestObject) (adminapi.GetSubscriptionSummaryResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) ListAdminAnnouncements(context.Context, adminapi.ListAdminAnnouncementsRequestObject) (adminapi.ListAdminAnnouncementsResponseObject, error) {
	return nil, errNotImplemented
}
