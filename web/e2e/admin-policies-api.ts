import type { AdminState, Schemas } from "./admin-data.ts";
import { long, pad, uuid } from "./admin-fixtures.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, type MockData, ok, paged } from "./mock.ts";

type PolicyState = {
  alerts: Schemas["AlertEvent"][];
  config: Schemas["AlertConfiguration"];
  documents: Schemas["LegalDocument"][];
  announcements: Map<string, Schemas["Announcement"]>;
  announcementCount: number;
  retention: Schemas["Retention"];
  rates: Map<string, number>;
};

const states = new WeakMap<AdminState, PolicyState>();
const at = "2026-11-01T08:00:00Z";
const manage = "sites.manage";
const created = <T>(json: T) => ({ status: 201 as const, json });

function policy(state: AdminState) {
  const current = states.get(state);
  if (!current) throw new Error("missing admin policy fixtures");
  return current;
}

function alertConfig(data: MockData): Schemas["AlertConfiguration"] {
  const worst = data === "worst";
  return {
    rev: 1,
    rules:
      data === "empty"
        ? []
        : [
            {
              id: "email-delivery",
              kind: "node_unhealthy",
              minimum_severity: "warning",
              threshold: 2,
              duration_seconds: 60,
              channel: "email",
              quiet_start: 0,
              quiet_end: 0,
              repeat_seconds: 600,
            },
          ],
    channels:
      data === "empty"
        ? []
        : [
            {
              id: "email",
              email: worst ? `${long("operations", 64)}@alerts.example.test` : "operations@example.test",
            },
            {
              id: "operations-webhook",
              webhook_url: worst
                ? `https://alerts.example.test/${long("notifications/", 200)}`
                : "https://alerts.example.test/axiom",
            },
          ],
    spend_detection: {
      enabled: true,
      notify_users: false,
      window_seconds: 900,
      baseline_seconds: 7200,
      min_spend_nano: 1_000_000_000,
      spend_ratio: 4,
      min_new_region_calls: 10,
      min_new_model_calls: 10,
    },
  };
}

export function startPolicies(state: AdminState, data: MockData) {
  const worst = data === "worst";
  states.set(state, {
    alerts:
      data === "empty"
        ? []
        : [
            {
              id: uuid(31, 1),
              kind: "node_unhealthy",
              key: worst ? long("unreachable-worker-", 180) : "worker-1",
              severity: "warning",
              occurrences: 2,
              state: "delivered",
              fired_at: at,
              fields: { node: "worker-1" },
              receipts: [
                { id: uuid(32, 1), rule_id: "email-delivery", sequence: 0, state: "delivered", at, delivered_at: at },
              ],
            },
          ],
    config: alertConfig(data),
    documents:
      data === "empty"
        ? []
        : [{ kind: "terms", locale: "zh", version: 1, body: "## Terms\n\nExample terms.", published_at: at }],
    announcements: new Map(),
    announcementCount: 0,
    retention: { hot_days: worst ? 3650 : 90 },
    rates: new Map(),
  });
}

function resolve({ params, state }: Ctx<"/api/admin/v1/alerts/events/{event_id}/resolve", "post">) {
  const event = policy(state).alerts.find((row) => row.id === params.event_id);
  if (!event) return fail(404, "not_found");
  event.state = "resolved";
  event.resolved_at = at;
  return created({ id: params.event_id });
}

function replace({ body, state }: Ctx<"/api/admin/v1/alerts/configuration", "put">) {
  const current = policy(state);
  if (body.rev !== current.config.rev) return fail(409, "stale_revision");
  const { reason: _reason, ...config } = body;
  current.config = { ...current.config, ...config, rev: body.rev + 1 };
  return created(current.config);
}

function rotate({ body, params, state }: Ctx<"/api/admin/v1/alerts/channels/{channel_id}/secret", "put">) {
  const { config } = policy(state);
  if (config.rev !== body.rev) return fail(409, "stale_revision");
  if (!config.channels?.some((item) => item.id === params.channel_id && item.webhook_url))
    return fail(404, "not_found");
  if (body.secret.length < 32) return fail(422, "unprocessable");
  config.rev++;
  return created({ id: uuid(32, 3) });
}

function publishAnnouncement({ body, state }: Ctx<"/api/admin/v1/announcements", "post">) {
  if (body.site_id !== state.siteId) return fail(404, "not_found");
  const current = policy(state);
  const { reason: _reason, ...content } = body;
  const id = `ann_${pad(++current.announcementCount)}`;
  const row = { id, ...content };
  current.announcements.set(id, row);
  return created(row);
}

function withdrawAnnouncement({
  params,
  state,
}: Ctx<"/api/admin/v1/announcements/{announcement_id}/withdraw", "post">) {
  const id = params.announcement_id;
  if (!id || !policy(state).announcements.delete(id)) return fail(404, "not_found");
  return created({ id });
}

function listDocuments({ params, query, state }: Ctx<"/api/admin/v1/sites/{site_id}/legal-documents", "get">) {
  if (params.site_id !== state.siteId) return fail(404, "not_found");
  const all = policy(state).documents.toSorted((a, b) => a.kind.localeCompare(b.kind) || b.version - a.version);
  const page = paged(all, query);
  return page ? ok({ documents: page.data, next_cursor: page.next_cursor }) : fail(400, "invalid_request");
}

function publishDocument({ params, body, state }: Ctx<"/api/admin/v1/sites/{site_id}/legal-documents", "post">) {
  if (params.site_id !== state.siteId) return fail(404, "not_found");
  const docs = policy(state).documents;
  const version = 1 + Math.max(0, ...docs.filter((doc) => doc.kind === body.kind).map((doc) => doc.version));
  const row = { kind: body.kind, locale: body.locale ?? "zh", body: body.body, published_at: at, version } as const;
  docs.push(row);
  return created(row);
}

function allowance(state: AdminState, id: string) {
  const requests = policy(state).rates.get(id) ?? null;
  return { requests, effective_requests: requests ?? 60 };
}

function setLimit({ body, params, state }: Ctx<"/api/admin/v1/rate-limits/{target}/{id}", "put">) {
  if (!params.id) return fail(404, "not_found");
  if (body.requests == null) policy(state).rates.delete(params.id);
  else policy(state).rates.set(params.id, body.requests);
  return ok(allowance(state, params.id));
}

export const policyHandlers: Handler<AdminState>[] = [
  route("get", `${base}/alerts/events`, reads(manage), ({ state, query }) => {
    const before = query.get("before");
    return ok({
      data: policy(state)
        .alerts.filter((event) => !before || (event.id ?? "") < before)
        .slice(0, 100),
    });
  }),
  route("post", `${base}/alerts/events/{event_id}/resolve`, changes(manage), resolve),
  route("get", `${base}/alerts/configuration`, reads(manage), ({ state }) => ok(policy(state).config)),
  route("put", `${base}/alerts/configuration`, changes(manage), replace),
  route("post", `${base}/alerts/tests`, changes(manage), ({ body, state }) => {
    const current = policy(state);
    if (!current.config.channels?.some((item) => item.id === body.channel)) return fail(404, "not_found");
    const id = uuid(31, current.alerts.length + 1);
    const receipt = uuid(32, current.alerts.length + 10);
    current.alerts.unshift({
      id,
      kind: "alert.test",
      key: id,
      severity: "info",
      state: "resolved",
      fired_at: at,
      resolved_at: at,
      receipts: [{ id: receipt, rule_id: "test", sequence: 0, state: "pending", at }],
    });
    return created({ id: receipt });
  }),
  route("put", `${base}/alerts/channels/{channel_id}/secret`, changes(manage), rotate),
  route("post", `${base}/announcements`, changes(manage), publishAnnouncement),
  route("post", `${base}/announcements/{announcement_id}/withdraw`, changes(manage), withdrawAnnouncement),
  route("get", `${base}/sites/{site_id}/legal-documents`, reads(manage), listDocuments),
  route("post", `${base}/sites/{site_id}/legal-documents`, changes(manage), publishDocument),
  route("get", `${base}/retention`, reads(manage), ({ state }) => ok(policy(state).retention)),
  route("put", `${base}/retention`, changes(manage), ({ body, state }) => {
    policy(state).retention = { hot_days: body.hot_days };
    return created(policy(state).retention);
  }),
  route("get", `${base}/rate-limits/{target}/{id}`, reads(manage), ({ params, state }) =>
    params.id ? ok(allowance(state, params.id)) : fail(404, "not_found"),
  ),
  route("put", `${base}/rate-limits/{target}/{id}`, changes(manage), setLimit),
];
