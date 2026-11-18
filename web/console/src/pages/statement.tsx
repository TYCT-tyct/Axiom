import { ApiError } from "@axiom/api";
import { useErrors, useSessionBound } from "@axiom/app";
import {
  Button,
  Card,
  Cluster,
  DataTable,
  FormError,
  Note,
  SegmentedControl,
  Stack,
  useFormat,
  usePageTitle,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { statementQuery } from "../queries";
import { save, statementCsv, statementFile } from "../statement";
import {
  browserTimeZone,
  dailyNetChange,
  type StatementDay,
  type StatementRow,
  statementDailyQuery,
} from "./bill-data";
import { BillPagedList, BillShell } from "./bill-shell";

type StatementViewMode = "movements" | "daily";

function StatementKindText({ kind }: { kind: StatementRow["kind"] }) {
  const { t } = useTranslation();
  const labels: Record<StatementRow["kind"], string> = {
    top_up: t("statement.kind.top_up"),
    gift: t("statement.kind.gift"),
    redeem: t("statement.kind.redeem"),
    spend: t("statement.kind.spend"),
    expiry: t("statement.kind.expiry"),
    refund: t("statement.kind.refund"),
    reversal: t("statement.kind.reversal"),
    freeze: t("statement.kind.freeze"),
    unfreeze: t("statement.kind.unfreeze"),
    chargeback: t("statement.kind.chargeback"),
    chargeback_reversal: t("statement.kind.chargeback_reversal"),
  };
  return <strong>{labels[kind]}</strong>;
}

function isCallReference(row: StatementRow) {
  return (row.kind === "spend" || row.kind === "reversal") && row.reference.length > 0;
}

function MovementKindCell({ row }: { row: StatementRow }) {
  const { t } = useTranslation();
  const shortRef = row.reference ? `${row.reference.slice(0, 8)}…` : "";
  return (
    <Stack gap={4}>
      <StatementKindText kind={row.kind} />
      {isCallReference(row) ? (
        <Link to="/calls/$callId" params={{ callId: row.reference }} title={row.reference}>
          {t("statement.call", { id: shortRef })}
        </Link>
      ) : shortRef ? (
        <span title={row.reference}>{shortRef}</span>
      ) : null}
    </Stack>
  );
}

function MovementsTable({ rows }: { rows: readonly StatementRow[] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DataTable<StatementRow>
      label={t("nav.statement")}
      rows={rows}
      rowKey={(row) => row.id}
      columns={[
        {
          key: "time",
          header: t("statement.col.time"),
          cell: (row) => format.dateTime(new Date(row.at)),
        },
        {
          key: "kind",
          header: t("statement.col.kind"),
          cell: (row) => <MovementKindCell row={row} />,
        },
        {
          key: "amount",
          header: t("statement.col.amount"),
          align: "end",
          cell: (row) => {
            const hold = row.kind === "freeze" || row.kind === "unfreeze";
            return (
              <Stack gap={4}>
                <span>{format.money(hold ? row.frozen_change : row.amount)}</span>
                {hold ? <span>{t("statement.frozenChange")}</span> : null}
              </Stack>
            );
          },
        },
        {
          key: "balance",
          header: t("statement.col.balance"),
          align: "end",
          cell: (row) => (
            <Stack gap={4}>
              {row.balance_after === null ? (
                <span title={t("statement.balanceUnknownNote")}>{t("statement.balanceUnknown")}</span>
              ) : (
                <span>{format.money(row.balance_after)}</span>
              )}
              <span>{t("statement.frozenAfter", { amount: format.money(row.frozen_after) })}</span>
            </Stack>
          ),
        },
      ]}
    />
  );
}

function MovementsView() {
  const { t } = useTranslation();
  const query = useInfiniteQuery(statementQuery);
  return (
    <BillPagedList
      query={query}
      title={t("statement.movements")}
      lede={t("statement.note")}
      emptyTitle={t("statement.empty.title")}
      emptyBody={t("statement.empty.body")}
      segmentTitle={t("statement.segment.title")}
      segmentBody={t("statement.segment.body")}
      emptyAction={
        <Button asChild variant="secondary">
          <Link to="/balance">{t("statement.empty.balance")}</Link>
        </Button>
      }
      renderTable={(rows) => <MovementsTable rows={rows} />}
    />
  );
}

function DayActivitySummary({ day }: { day: StatementDay }) {
  const { t } = useTranslation();
  const format = useFormat();
  const parts: string[] = [];
  if (BigInt(day.topped_up.amount) > 0n)
    parts.push(t("statement.bill.daily.toppedUp", { amount: format.money(day.topped_up) }));
  if (BigInt(day.gifted.amount) > 0n)
    parts.push(t("statement.bill.daily.gifted", { amount: format.money(day.gifted) }));
  if (BigInt(day.spent.amount) > 0n) parts.push(t("statement.bill.daily.spent", { amount: format.money(day.spent) }));
  if (BigInt(day.expired.amount) > 0n)
    parts.push(t("statement.bill.daily.expired", { amount: format.money(day.expired) }));
  return <span>{parts.length > 0 ? parts.join(" · ") : t("statement.bill.daily.noActivity")}</span>;
}

function DailyTable({ rows }: { rows: readonly StatementDay[] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DataTable<StatementDay>
      label={t("statement.bill.daily.title")}
      rows={rows}
      rowKey={(day) => day.day}
      columns={[
        { key: "day", header: t("statement.bill.daily.col.day"), cell: (day) => day.day },
        {
          key: "summary",
          header: t("statement.bill.daily.col.summary"),
          cell: (day) => <DayActivitySummary day={day} />,
        },
        {
          key: "closing",
          header: t("statement.bill.daily.col.closing"),
          align: "end",
          cell: (day) =>
            day.closing_balance === null ? t("statement.balanceUnknown") : format.money(day.closing_balance),
        },
        {
          key: "net",
          header: t("statement.bill.daily.col.net"),
          align: "end",
          cell: (day) => format.money({ amount: dailyNetChange(day), currency: day.spent.currency }),
        },
      ]}
    />
  );
}

function DailyView() {
  const { t } = useTranslation();
  const timeZone = browserTimeZone();
  const query = useInfiniteQuery({
    ...statementDailyQuery(timeZone),
    retry: (count, error) =>
      !(error instanceof ApiError && (error.status === 404 || error.status === 400)) && count < 2,
  });

  if (query.error instanceof ApiError && query.error.status === 404) {
    return (
      <Card title={t("statement.bill.daily.title")}>
        <Note title={t("statement.bill.daily.unavailable")} />
      </Card>
    );
  }
  if (query.error instanceof ApiError && query.error.status === 400) {
    return (
      <Card title={t("statement.bill.daily.title")}>
        <Note title={t("statement.bill.daily.tooMany")} />
      </Card>
    );
  }

  return (
    <BillPagedList
      query={query}
      title={t("statement.bill.daily.title")}
      lede={t("statement.bill.daily.note", { tz: timeZone })}
      emptyTitle={t("statement.bill.daily.empty")}
      emptyBody={t("statement.bill.daily.emptyBody")}
      renderTable={(rows) => <DailyTable rows={rows} />}
    />
  );
}

export default function StatementPage() {
  const { t } = useTranslation();
  const errors = useErrors();
  const bound = useSessionBound();
  const [view, setView] = useState<StatementViewMode>("movements");
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);
  usePageTitle(t("nav.statement"));

  const handleExport = () => {
    setExporting(true);
    setExportError(null);
    void bound(async (signal) => statementFile(await statementCsv(signal)))
      .then((res) => res && save(res.value))
      .catch((error) => setExportError(errors.message(error)))
      .finally(() => setExporting(false));
  };

  return (
    <BillShell activeTab="statement" title={t("nav.statement")} subtitle={t("statement.description")}>
      <Cluster justify="between" gap={12}>
        <SegmentedControl
          label={t("statement.bill.daily.viewLabel")}
          value={view}
          options={[
            { value: "movements", label: t("statement.bill.daily.movements") },
            { value: "daily", label: t("statement.bill.daily.daily") },
          ]}
          onChange={setView}
        />
        <Button variant="secondary" size="sm" disabled={exporting} onClick={handleExport}>
          {t("statement.export")}
        </Button>
      </Cluster>
      {exportError ? <FormError>{t("statement.exportFailed", { reason: exportError })}</FormError> : null}
      {view === "movements" ? <MovementsView /> : <DailyView />}
    </BillShell>
  );
}
