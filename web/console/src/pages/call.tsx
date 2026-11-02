import { ApiError, type Schemas } from "@axiom/api";
import { Button, CopyButton, DataState, EmptyState, Icon, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CallStatus } from "../parts";
import { callQuery } from "../queries";

export default function CallDetail() {
  const { t } = useTranslation();
  const { callId } = useParams({ from: "/app/calls/$callId" });
  const call = useQuery(callQuery(callId));
  const missing = call.error instanceof ApiError && call.error.status === 404;
  return (
    <>
      <PageHeader title={t("call.title")} description={t("call.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Button variant="ghost" asChild className="-ml-3">
          <Link to="/calls" search={{ model: undefined, client_app: undefined, since: undefined, until: undefined }}>
            <Icon name="left" />
            {t("call.back")}
          </Link>
        </Button>
        {missing ? (
          <EmptyState title={t("call.notFound")}>{t("call.notFoundBody")}</EmptyState>
        ) : (
          <DataState query={call}>{(data) => <Details call={data} />}</DataState>
        )}
      </div>
    </>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-x-4 gap-y-0.5 px-4 py-2.5 sm:grid-cols-[12rem_minmax(0,1fr)]">
      <dt className="text-t3">{label}</dt>
      <dd className="min-w-0 text-t1 [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

// Text a call did not have is the empty string in the contract, and 0 for a first token that never came.
const orDash = (value: string) => value || "—";

function Details({ call }: { call: Schemas["Call"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const usage = Object.entries(call.usage);
  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <Panel title={t("call.summary")}>
        <dl className="divide-y divide-b1">
          <Row label={t("call.status")}>
            <span className="inline-flex flex-wrap items-center gap-2">
              <CallStatus errorKind={call.error_kind} />
              <span className="font-mono">{call.error_kind}</span>
            </span>
          </Row>
          <Row label={t("call.id")}>
            <span className="flex flex-wrap items-center gap-2">
              <span className="font-mono">{call.id}</span>
              <CopyButton text={call.id} />
            </span>
          </Row>
          <Row label={t("call.at")}>{format.dateTime(new Date(call.at))}</Row>
          <Row label={t("call.model")}>{call.model}</Row>
          <Row label={t("call.app")}>{orDash(call.client_app)}</Row>
          <Row label={t("call.requestClass")}>{orDash(call.request_class)}</Row>
          <Row label={t("call.serviceTier")}>{orDash(call.service_tier)}</Row>
          <Row label={t("call.finish")}>{orDash(call.finish_reason)}</Row>
          {call.cache_hit ? <Row label={t("call.cache")}>{t("call.cacheHit")}</Row> : null}
          <Row label={t("call.latency")}>{t("calls.ms", { value: format.number(call.latency_ms) })}</Row>
          <Row label={t("call.firstToken")}>
            {call.first_token_ms === 0 ? "—" : t("calls.ms", { value: format.number(call.first_token_ms) })}
          </Row>
          <Row label={t("call.session")}>{orDash(call.session_id)}</Row>
          <Row label={t("call.subagent")}>{orDash(call.subagent_id)}</Row>
          <Row label={t("call.key")}>{call.key_id}</Row>
          <Row label={t("call.ip")}>{orDash(call.ip)}</Row>
        </dl>
      </Panel>
      <div className="space-y-4">
        <Panel title={t("call.cost")}>
          <p className="px-4 pt-4 text-xl text-t1 tabular-nums [overflow-wrap:anywhere]">{format.money(call.price)}</p>
          {call.cache_hit ? (
            <p className="px-4 pt-1 pb-4 text-ui text-t3">{t("call.cacheHitBody")}</p>
          ) : call.free ? (
            <p className="px-4 pt-1 pb-4 text-ui text-t3">{t("call.free")}</p>
          ) : null}
        </Panel>
        <Panel title={t("call.usage")}>
          {usage.length === 0 ? (
            <p className="px-4 py-3 text-t3">{t("call.noUsage")}</p>
          ) : (
            <dl className="divide-y divide-b1">
              {usage.map(([item, amount]) => (
                <div key={item} className="flex justify-between gap-4 px-4 py-2.5">
                  <dt className="font-mono text-t3 [overflow-wrap:anywhere]">{item}</dt>
                  <dd className="text-t1 tabular-nums">{format.number(amount)}</dd>
                </div>
              ))}
            </dl>
          )}
        </Panel>
      </div>
    </div>
  );
}
