import type { AdminSchemas } from "@axiom/api";
import { can, Stat, useSession } from "@axiom/app";
import {
  CopyButton,
  DataState,
  EmptyState,
  IdText,
  Notice,
  PageHeader,
  Panel,
  Segmented,
  Table,
  Td,
  Th,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useNavigate, useSearch } from "@tanstack/react-router";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { nav } from "../nav";
import {
  callsDrill,
  type OverviewRange,
  ordersDrill,
  overviewRanges,
  overviewWindow,
  paidOrdersDrill,
  retentionOrdersDrill,
  type Window,
} from "../overview";
import { mainCell, PageBody } from "../parts";
import { overviewQuery } from "../queries";
import { formatTargetRate, targetsEmpty } from "../targets";
import { TargetNote, TargetsAction } from "../targets-view";

// The figures are links: each opens the call or order list narrowed to the same window, so a number is never a dead
// end. The whole card is the target, and the keyboard sees one focus ring around it; the pointer sees its border lift.
const figure =
  "block h-full rounded-card focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent [&>section]:h-full [&>section]:transition-colors [&>section]:duration-150 hover:[&>section]:border-b3";
// The counts share the first row and the money the second: five cards in one row left about 190px at 1280px, too
// little for an amount, which then broke in the middle of its digits.
const count = `${figure} lg:col-span-3`;
const amount = `${figure} lg:col-span-2`;

// The admin's home is the operations dashboard: the site's calls, what they earned and cost, the cash that moved, the
// paid top-ups that made accounts paying customers, the margin between call revenue and supplier cost, retention, and
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
        <DataState query={overview}>{(data) => <Figures data={data} window={window} anchor={anchor} />}</DataState>
      </PageBody>
    </>
  );
}

type Overview = AdminSchemas["OperationsOverview"];

// CoverageNotice says whether the call log covers the window: a node that cannot tell says so, and an archived month
// marks only the log's part of the dashboard incomplete.
function CoverageNotice({ data }: { data: Overview }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (data.calls_incomplete === undefined) {
    return <Notice>{t("overview.coverageUnavailable")}</Notice>;
  }
  if (!data.calls_incomplete) {
    return null;
  }
  return (
    <Notice>
      {data.calls_available_since
        ? t("overview.callsIncomplete", { since: format.dateTime(new Date(data.calls_available_since)) })
        : t("overview.callsIncompleteUnknown")}
    </Notice>
  );
}

// TargetsSection is the description, month picker and editor, or the one notice that this node does not have targets.
// An empty set still shows the editor: the month can be given its first target.
function TargetsSection({
  targets,
  anchor,
  currency,
}: {
  targets?: AdminSchemas["OperationsTargetComparison"];
  anchor: Date;
  currency: string;
}) {
  const { t } = useTranslation();
  if (targets === undefined) {
    return <Notice>{t("overview.targets.unavailable")}</Notice>;
  }
  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <p className="max-w-3xl text-ui text-t3">{t("overview.targets.description")}</p>
        {currency ? <TargetsAction anchor={anchor} currency={currency} /> : null}
      </div>
      {targetsEmpty(targets) ? <Notice>{t("overview.targets.empty")}</Notice> : null}
    </>
  );
}

// withTarget puts a figure's target under its existing note; with no targets in the window, the cards keep their notes
// and one notice above them explains the empty set.
function withTarget(show: boolean, target: ReactNode, base?: ReactNode): ReactNode {
  if (!show) {
    return base;
  }
  return base ? (
    <>
      <span className="block">{base}</span>
      {target}
    </>
  ) : (
    target
  );
}

type FigureProps = {
  data: Overview;
  window: Window;
  site?: string;
  targets?: AdminSchemas["OperationsTargetComparison"];
  showTarget: boolean;
};

function Figures({ data, window, anchor }: { data: Overview; window: Window; anchor: Date }) {
  const targets = data.targets;
  const targetNotes = targets !== undefined && !targetsEmpty(targets);
  const currency = data.revenue?.currency ?? data.cost?.currency ?? data.monthly_revenue?.currency ?? "";
  const site = data.site_id;
  return (
    <>
      <CoverageNotice data={data} />
      <TargetsSection targets={targets} anchor={anchor} currency={currency} />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-6">
        <CallsFigure data={data} window={window} site={site} targets={targets} showTarget={targetNotes} />
        <ErrorsFigure data={data} window={window} site={site} targets={targets} showTarget={targetNotes} />
        <RevenueFigure data={data} window={window} site={site} targets={targets} showTarget={targetNotes} />
        <CostFigure data={data} window={window} site={site} targets={targets} showTarget={targetNotes} />
        <CashFigure data={data} window={window} showTarget={targetNotes} />
      </div>
      <BusinessFigures data={data} window={window} site={site} targets={targets} targetNotes={targetNotes} />
      <div className="grid gap-4 xl:grid-cols-2">
        <TopAccounts data={data} window={window} site={site} />
        <TopModels data={data} window={window} site={site} />
      </div>
    </>
  );
}

function BusinessFigures({
  data,
  window,
  site,
  targets,
  targetNotes,
}: {
  data: Overview;
  window: Window;
  site?: string;
  targets?: AdminSchemas["OperationsTargetComparison"];
  targetNotes: boolean;
}) {
  const { t } = useTranslation();
  return (
    <>
      <h2 className="text-ui font-medium text-t2">{t("overview.business")}</h2>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <PaidUsersFigure
          data={data}
          window={window}
          targets={targets}
          showTarget={targetNotes && (data.paid_users !== undefined || targets?.paid_users !== undefined)}
        />
        <MonthlyRevenueFigure
          data={data}
          window={window}
          targets={targets}
          showTarget={targetNotes && (data.monthly_revenue !== undefined || targets?.monthly_revenue !== undefined)}
        />
        <GrossMarginFigure
          data={data}
          window={window}
          site={site}
          targets={targets}
          showTarget={targetNotes && (data.gross_margin !== undefined || targets?.gross_margin !== undefined)}
        />
        <RetentionFigure
          data={data}
          window={window}
          targets={targets}
          showTarget={targetNotes && (data.retention !== undefined || targets?.retention !== undefined)}
        />
      </div>
    </>
  );
}

function CallsFigure({ data, window, site, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Link to="/calls" search={callsDrill(window, { site })} className={count}>
      <Stat
        label={t("overview.calls")}
        value={data.calls === undefined ? t("overview.unavailable") : format.number(data.calls)}
        note={withTarget(showTarget, <TargetNote metric="calls" comparison={targets?.calls} />)}
      />
    </Link>
  );
}

function ErrorsFigure({ data, window, site, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Link to="/calls" search={callsDrill(window, { site, status: "error" })} className={count}>
      <Stat
        label={t("overview.errors")}
        value={data.errors === undefined ? t("overview.unavailable") : format.number(data.errors)}
        note={withTarget(showTarget, <TargetNote metric="errors" comparison={targets?.errors} />)}
      />
    </Link>
  );
}

function RevenueFigure({ data, window, site, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Link to="/calls" search={callsDrill(window, { site })} className={amount}>
      <Stat
        label={t("overview.revenue")}
        value={data.revenue ? format.money(data.revenue) : t("overview.unavailable")}
        note={withTarget(
          showTarget,
          <TargetNote metric="revenue" comparison={targets?.revenue} />,
          t("overview.revenueNote"),
        )}
      />
    </Link>
  );
}

function CostFigure({ data, window, site, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  const note = data.unknown_cost_calls
    ? t("overview.unknownCost", { calls: format.number(data.unknown_cost_calls) })
    : t("overview.costNote");
  return (
    <Link to="/calls" search={callsDrill(window, { site })} className={amount}>
      <Stat
        label={t("overview.cost")}
        value={data.cost ? format.money(data.cost) : t("overview.unavailable")}
        note={withTarget(
          showTarget,
          <TargetNote metric="cost" comparison={targets?.cost} costUnknown={Boolean(data.unknown_cost_calls)} />,
          note,
        )}
      />
    </Link>
  );
}

function CashFigure({ data, window, showTarget }: { data: Overview; window: Window; showTarget: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  // One line per currency: joined on one line, a second amount broke in the middle of its digits.
  const cash = data.cash?.length
    ? data.cash.map((amount) => (
        <span key={amount.currency} className="block">
          {format.money(amount)}
        </span>
      ))
    : t("overview.unavailable");
  return (
    <Link to="/orders" search={ordersDrill(window)} className={amount}>
      <Stat
        label={t("overview.cash")}
        value={cash}
        note={withTarget(
          showTarget,
          <span className="block">{t("overview.targets.cashNoTarget")}</span>,
          t("overview.cashNote"),
        )}
      />
    </Link>
  );
}

function PaidOrdersFigure({ window, children }: { window: Window; children: ReactNode }) {
  return (
    <Link to="/orders" search={paidOrdersDrill(window)} className={figure}>
      {children}
    </Link>
  );
}

function PaidUsersFigure({ data, window, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <PaidOrdersFigure window={window}>
      <Stat
        label={t("overview.paidUsers")}
        value={data.paid_users === undefined ? t("overview.unavailable") : format.number(data.paid_users)}
        note={withTarget(
          showTarget,
          <TargetNote metric="paid_users" comparison={targets?.paid_users} />,
          t("overview.paidUsersNote"),
        )}
      />
    </PaidOrdersFigure>
  );
}

function MonthlyRevenueFigure({ data, window, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <PaidOrdersFigure window={window}>
      <Stat
        label={t("overview.monthlyRevenue")}
        value={data.monthly_revenue ? format.money(data.monthly_revenue) : t("overview.unavailable")}
        note={withTarget(
          showTarget,
          <TargetNote metric="monthly_revenue" comparison={targets?.monthly_revenue} />,
          t("overview.monthlyRevenueNote"),
        )}
      />
    </PaidOrdersFigure>
  );
}

function GrossMarginFigure({ data, window, site, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  return (
    <Link to="/calls" search={callsDrill(window, { site })} className={figure}>
      <Stat
        label={t("overview.grossMargin")}
        value={data.gross_margin === undefined ? t("overview.unavailable") : formatTargetRate(data.gross_margin)}
        note={withTarget(
          showTarget,
          <TargetNote metric="gross_margin" comparison={targets?.gross_margin} />,
          t("overview.grossMarginNote"),
        )}
      />
    </Link>
  );
}

function RetentionFigure({ data, window, targets, showTarget }: FigureProps) {
  const { t } = useTranslation();
  const format = useFormat();
  const retention = data.retention;
  const note = retention
    ? retention.cohort === 0
      ? t("overview.retentionEmpty")
      : t("overview.retentionNote", {
          retained: format.number(retention.retained),
          cohort: format.number(retention.cohort),
        })
    : t("overview.retentionUnavailable");
  return (
    <Link to="/orders" search={retentionOrdersDrill(window)} className={figure}>
      <Stat
        label={t("overview.retention")}
        value={retention?.rate === undefined ? t("overview.unavailable") : formatTargetRate(retention.rate)}
        note={withTarget(showTarget, <TargetNote metric="retention" comparison={targets?.retention} />, note)}
      />
    </Link>
  );
}

function RankHead({ first }: { first: string }) {
  const { t } = useTranslation();
  return (
    <thead>
      <tr>
        <Th>{first}</Th>
        <Th className="text-right">{t("overview.col.calls")}</Th>
        <Th className="text-right">{t("overview.col.errors")}</Th>
      </tr>
    </thead>
  );
}

function TopAccounts({ data, window, site }: { data: Overview; window: Window; site?: string }) {
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
                <Td className={mainCell}>
                  {account.account_id ? (
                    <span className="flex min-w-0 items-center gap-1">
                      <Link
                        to="/calls"
                        search={callsDrill(window, { site, account: account.account_id })}
                        className="inline-flex min-w-0 text-accent underline-offset-4 hover:underline"
                      >
                        <IdText value={account.account_id} />
                      </Link>
                      <CopyButton compact text={account.account_id} />
                    </span>
                  ) : (
                    "—"
                  )}
                </Td>
                <Td className="whitespace-nowrap text-right tabular-nums">{format.number(account.calls ?? 0)}</Td>
                <Td className="whitespace-nowrap text-right tabular-nums">{format.number(account.errors ?? 0)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  );
}

function TopModels({ data, window, site }: { data: Overview; window: Window; site?: string }) {
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
                <Td className={mainCell}>
                  {model.model ? (
                    <span className="flex min-w-0 items-center gap-1">
                      <Link
                        to="/calls"
                        search={callsDrill(window, { site, model: model.model })}
                        className="inline-flex min-w-0 text-accent underline-offset-4 hover:underline"
                      >
                        <IdText value={model.model} />
                      </Link>
                      <CopyButton compact text={model.model} />
                    </span>
                  ) : (
                    "—"
                  )}
                </Td>
                <Td className="whitespace-nowrap text-right tabular-nums">{format.number(model.calls ?? 0)}</Td>
                <Td className="whitespace-nowrap text-right tabular-nums">{format.number(model.errors ?? 0)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  );
}
