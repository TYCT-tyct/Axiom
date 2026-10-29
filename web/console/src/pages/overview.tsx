import type { Schemas } from "@axiom/api";
import { Stat } from "@axiom/app";
import { Badge, DataState, EmptyState, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { maskedKey } from "../parts";
import { balanceQuery, keysQuery, recentErrorsQuery } from "../queries";

export default function Overview() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("nav.overview")} description={t("overview.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Figures />
        <div className="grid gap-4 xl:grid-cols-2">
          <KeysPanel />
          <ErrorsPanel />
        </div>
      </div>
    </>
  );
}

// The balance and the month's spending are the first things on the page, before any action (docs/frontend.md「可用性任务」).
function Figures() {
  const { t } = useTranslation();
  const format = useFormat();
  const balance = useQuery(balanceQuery);
  return (
    <DataState query={balance} compact>
      {(data) => (
        <div className="grid gap-4 sm:grid-cols-2">
          <Stat
            label={t("balance.available")}
            value={format.money(data.available)}
            note={
              <Link to="/balance" className="text-accent underline-offset-4 hover:underline">
                {t("overview.balanceDetails")}
              </Link>
            }
          />
          <Stat
            label={t("balance.spentThisMonth")}
            value={format.money(data.spent_this_month)}
            note={t("overview.monthNote")}
          />
        </div>
      )}
    </DataState>
  );
}

function KeysPanel() {
  const { t } = useTranslation();
  const keys = useInfiniteQuery(keysQuery);
  return (
    <Panel
      title={t("nav.keys")}
      action={
        <Link to="/keys" className="text-ui text-accent underline-offset-4 hover:underline">
          {t("overview.all")}
        </Link>
      }
    >
      <DataState
        query={keys}
        compact
        isEmpty={(data) => data.pages[0]?.data.length === 0}
        empty={
          <EmptyState compact title={t("overview.noKeys")}>
            <Link to="/keys" className="text-accent underline-offset-4 hover:underline">
              {t("overview.createFirst")}
            </Link>
          </EmptyState>
        }
      >
        {(data) => (
          <ul className="divide-y divide-b1">
            {(data.pages[0]?.data ?? []).slice(0, 5).map((key) => (
              <KeyItem key={key.id} apiKey={key} />
            ))}
          </ul>
        )}
      </DataState>
    </Panel>
  );
}

function KeyItem({ apiKey }: { apiKey: Schemas["Key"] }) {
  const { t } = useTranslation();
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <div className="min-w-0">
        <p className="truncate text-t1" title={apiKey.name}>
          {apiKey.name}
        </p>
        <p className="font-mono text-label text-t4">{maskedKey(apiKey)}</p>
      </div>
      <Badge tone={apiKey.disabled ? "warn" : "ok"} icon={apiKey.disabled ? "alert" : "check"}>
        {apiKey.disabled ? t("keys.disabled") : t("keys.enabled")}
      </Badge>
    </li>
  );
}

// The server selects failures before limiting, so successful traffic cannot hide an older error.
function ErrorsPanel() {
  const { t } = useTranslation();
  const format = useFormat();
  const calls = useQuery(recentErrorsQuery);
  return (
    <Panel title={t("overview.recentErrors")}>
      <DataState
        query={calls}
        compact
        isEmpty={(data) => data.status_filter === "error" && data.data.length === 0}
        empty={<EmptyState compact title={t("overview.noErrors")} />}
      >
        {(data) =>
          data.status_filter !== "error" ? (
            <EmptyState compact title={t("overview.errorsUnavailable")} />
          ) : (
            <ul className="divide-y divide-b1">
              {data.data.map((call) => (
                <li key={call.id} className="px-4 py-2.5">
                  <Link to="/calls/$callId" params={{ callId: call.id }} className="block hover:text-t1">
                    <p className="truncate text-t1" title={call.model}>
                      {call.model}
                    </p>
                    <p className="flex flex-wrap gap-x-2 text-label text-t3 [overflow-wrap:anywhere]">
                      <span className="font-mono text-bad">{call.error_kind}</span>
                      <span>{format.dateTime(new Date(call.at))}</span>
                    </p>
                  </Link>
                </li>
              ))}
            </ul>
          )
        }
      </DataState>
    </Panel>
  );
}
