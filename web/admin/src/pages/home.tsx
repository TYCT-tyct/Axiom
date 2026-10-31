import type { AdminSchemas } from "@axiom/api";
import { can, Stat, useSession } from "@axiom/app";
import { DataState, EmptyState, PageHeader, Panel, Segmented, Table, Td, Th, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { nav } from "../nav";
import { callsDrill, type OverviewRange, ordersDrill, overviewRanges, overviewWindow, type Window } from "../overview";
import { Code, PageBody } from "../parts";
import { overviewQuery } from "../queries";

// The figures are links: each opens the call or order list narrowed to the same window, so a number is never a dead
// end. The whole card is the target, and the keyboard sees one focus ring around it.
const figure =
  "block h-full rounded-card focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent [&>section]:h-full";

// The admin's home is the operations dashboard: the site's calls, what they earned and cost, the cash that moved, and
// the accounts and models behind them (docs/domains/operations.md「运营看板」). A session without sites.manage sees the
// first page it may use instead, as before.
export default function Home() {
  const { t } = useTranslation();
  const session = useSession();
  if (!can(session, "sites.manage")) {
    const first = nav.find((item) => item.to !== "/" && can(session, item.permission));
    if (first) {
      return <Navigate to={first.to} replace />;
    }
    return <PageHeader title={t("home.noAccessTitle")} description={t("home.noAccessBody")} />;
  }
  return <Dashboard />;
}

function Dashboard() {
  const { t } = useTranslation();
  const format = useFormat();
  const { range } = useSearch({ from: "/app/" });
  const navigate = useNavigate();
  // The visit's instant, not the render's: a refetch or a range change must not move the window under the figures.
  const [anchor] = useState(() => new Date());
  const window = useMemo(() => overviewWindow(range, anchor), [range, anchor]);
  const overview = useQuery(overviewQuery(window.since, window.until));
  return (
    <>
      <PageHeader title={t("home.title")} description={t("home.description")} />
      <PageBody>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Segmented<OverviewRange>
            label={t("overview.rangeLabel")}
            value={range}
            options={overviewRanges.map((value) => ({ value, label: t(`overview.range.${value}`) }))}
            onChange={(value) => navigate({ to: "/", search: { range: value }, replace: true })}
          />
          <p className="text-label text-t3">
            {t("overview.window", {
              since: format.dateTime(new Date(window.since)),
              until: format.dateTime(new Date(window.until)),
            })}
          </p>
        </div>
        <DataState query={overview}>{(data) => <Figures data={data} window={window} />}</DataState>
      </PageBody>
    </>
  );
}

type Overview = AdminSchemas["OperationsOverview"];

function Figures({ data, window }: { data: Overview; window: Window }) {
  const { t } = useTranslation();
  const format = useFormat();
  const count = (n?: number) => (n === undefined ? t("overview.unavailable") : format.number(n));
  const money = (m?: AdminSchemas["Money"]) => (m ? format.money(m) : t("overview.unavailable"));
  const cash = data.cash?.length ? data.cash.map((c) => format.money(c)).join(" · ") : t("overview.unavailable");
  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <Link to="/calls" search={callsDrill(window)} className={figure}>
          <Stat label={t("overview.calls")} value={count(data.calls)} />
        </Link>
        <Link to="/calls" search={callsDrill(window, { status: "error" })} className={figure}>
          <Stat label={t("overview.errors")} value={count(data.errors)} />
        </Link>
        <Link to="/calls" search={callsDrill(window)} className={figure}>
          <Stat label={t("overview.revenue")} value={money(data.revenue)} note={t("overview.revenueNote")} />
        </Link>
        <Link to="/calls" search={callsDrill(window)} className={figure}>
          <Stat
            label={t("overview.cost")}
            value={money(data.cost)}
            note={
              data.unknown_cost_calls
                ? t("overview.unknownCost", { calls: format.number(data.unknown_cost_calls) })
                : t("overview.costNote")
            }
          />
        </Link>
        <Link to="/orders" search={ordersDrill(window)} className={figure}>
          <Stat label={t("overview.cash")} value={cash} note={t("overview.cashNote")} />
        </Link>
      </div>
      <div className="grid gap-4 xl:grid-cols-2">
        <TopAccounts data={data} window={window} />
        <TopModels data={data} window={window} />
      </div>
    </>
  );
}

function RankHead({ first }: { first: string }) {
  const { t } = useTranslation();
  return (
    <thead>
      <tr>
        <Th>{first}</Th>
        <Th>{t("overview.col.calls")}</Th>
        <Th>{t("overview.col.errors")}</Th>
      </tr>
    </thead>
  );
}

function TopAccounts({ data, window }: { data: Overview; window: Window }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Panel title={t("overview.topAccounts")}>
      {data.top_accounts === undefined ? (
        <p className="p-4 text-t3">{t("overview.unavailable")}</p>
      ) : data.top_accounts.length === 0 ? (
        <EmptyState compact title={t("overview.noAccounts.title")}>
          {t("overview.noAccounts.body")}
        </EmptyState>
      ) : (
        <Table label={t("overview.topAccounts")}>
          <RankHead first={t("overview.col.account")} />
          <tbody>
            {data.top_accounts.map((account) => (
              <tr key={account.account_id}>
                <Td>
                  <Link
                    to="/calls"
                    search={callsDrill(window, { account: account.account_id })}
                    className="text-accent underline-offset-4 hover:underline"
                  >
                    <Code>{account.account_id}</Code>
                  </Link>
                </Td>
                <Td className="tabular-nums">{format.number(account.calls ?? 0)}</Td>
                <Td className="tabular-nums">{format.number(account.errors ?? 0)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  );
}

function TopModels({ data, window }: { data: Overview; window: Window }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Panel title={t("overview.topModels")}>
      {data.top_models === undefined ? (
        <p className="p-4 text-t3">{t("overview.unavailable")}</p>
      ) : data.top_models.length === 0 ? (
        <EmptyState compact title={t("overview.noModels.title")}>
          {t("overview.noModels.body")}
        </EmptyState>
      ) : (
        <Table label={t("overview.topModels")}>
          <RankHead first={t("overview.col.model")} />
          <tbody>
            {data.top_models.map((model) => (
              <tr key={model.model}>
                <Td>
                  <Link
                    to="/calls"
                    search={callsDrill(window, { model: model.model })}
                    className="text-accent underline-offset-4 hover:underline"
                  >
                    <Code>{model.model}</Code>
                  </Link>
                </Td>
                <Td className="tabular-nums">{format.number(model.calls ?? 0)}</Td>
                <Td className="tabular-nums">{format.number(model.errors ?? 0)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  );
}
