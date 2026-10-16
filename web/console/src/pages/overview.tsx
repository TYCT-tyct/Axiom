import type { Schemas } from "@axiom/api";
import { Badge, DataState, EmptyState, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { maskedKey } from "../parts";
import { balanceQuery, callsQuery, keysQuery } from "../queries";
import { Stat } from "../stat";

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

// The latest failures among the newest calls, with the call to open for the reason.
function ErrorsPanel() {
  const { t } = useTranslation();
  const format = useFormat();
  const calls = useInfiniteQuery(callsQuery({}));
  return (
    <Panel title={t("overview.recentErrors")}>
      <DataState
        query={calls}
        compact
        isEmpty={(data) => !data.pages[0]?.data.some((call) => call.error_kind !== "")}
        empty={<EmptyState compact title={t("overview.noErrors")} />}
      >
        {(data) => (
          <ul className="divide-y divide-b1">
            {(data.pages[0]?.data ?? [])
              .filter((call) => call.error_kind !== "")
              .slice(0, 5)
              .map((call) => (
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
        )}
      </DataState>
    </Panel>
  );
}
