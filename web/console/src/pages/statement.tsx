import type { Schemas } from "@axiom/api";
import { LoadMore, useErrors } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, Th, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { memo, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { statementQuery } from "../queries";
import { raised, save, statementCsv, statementFile } from "../statement";

export default function Statement() {
  const { t } = useTranslation();
  const statement = useInfiniteQuery(statementQuery);
  const [failed, setFailed] = useState<unknown>(null);
  const rows = useMemo(() => statement.data?.pages.flatMap((page) => page.data) ?? [], [statement.data]);
  return (
    <>
      <PageHeader title={t("nav.statement")} description={t("statement.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Panel title={t("statement.movements")} action={<Export onFailed={setFailed} />}>
          {failed ? <ExportFailed error={failed} /> : null}
          <DataState
            query={statement}
            isEmpty={() => rows.length === 0}
            empty={<NoMovements more={statement.hasNextPage} />}
          >
            {() => <StatementTable rows={rows} />}
          </DataState>
          <LoadMore list={statement} />
        </Panel>
        <p className="max-w-prose text-ui text-t3">{t("statement.note")}</p>
      </div>
    </>
  );
}

function NoMovements({ more }: { more: boolean }) {
  const { t } = useTranslation();
  return (
    <EmptyState title={t(more ? "statement.segment.title" : "statement.empty.title")}>
      {t(more ? "statement.segment.body" : "statement.empty.body")}
      <p className="mt-2">
        <Link to="/balance" className="text-accent underline-offset-4 hover:underline">
          {t("statement.empty.balance")}
        </Link>
      </p>
    </EmptyState>
  );
}

// Export downloads the statement as a CSV file. The request is the page's own, not a query: it runs once, when asked
// for, and a second press waits for the first.
function Export({ onFailed }: { onFailed: (error: unknown) => void }) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  const start = () => {
    setBusy(true);
    onFailed(null);
    statementCsv()
      .then((csv) => save(statementFile(csv)))
      .catch(onFailed)
      .finally(() => setBusy(false));
  };
  return (
    <Button size="sm" onClick={start} disabled={busy} aria-busy={busy}>
      {busy ? <Icon name="refresh" className="animate-spin" /> : null}
      {t("statement.export")}
    </Button>
  );
}

function ExportFailed({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <p role="alert" className="border-b border-b1 px-4 py-2 text-ui text-bad [overflow-wrap:anywhere]">
      {t("statement.exportFailed", { reason: errors.message(error) })}
    </p>
  );
}

function StatementTable({ rows }: { rows: Schemas["StatementRow"][] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.statement")}>
      <thead>
        <tr>
          <Th>{t("statement.col.time")}</Th>
          <Th>{t("statement.col.kind")}</Th>
          <Th className="text-right">{t("statement.col.amount")}</Th>
          <Th className="text-right">{t("statement.col.balance")}</Th>
          <Th className="hidden lg:table-cell">{t("statement.col.reference")}</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <MovementRow key={row.id} row={row} />
        ))}
      </tbody>
    </Table>
  );
}

const MovementRow = memo(function MovementRow({ row }: { row: Schemas["StatementRow"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const credit = raised(row.amount.amount);
  const hold = row.kind === "freeze" || row.kind === "unfreeze";
  return (
    <tr className="hover:bg-s2">
      <Td className="whitespace-nowrap">{format.dateTime(new Date(row.at))}</Td>
      <Td className="whitespace-nowrap">{t(`statement.kind.${row.kind}`)}</Td>
      <Td
        className={
          credit ? "text-right whitespace-nowrap tabular-nums text-ok" : "text-right whitespace-nowrap tabular-nums"
        }
      >
        {credit ? "+" : ""}
        {format.money(hold ? row.frozen_change : row.amount)}
        {hold ? <span className="block text-label text-t3">{t("statement.frozenChange")}</span> : null}
      </Td>
      <Td className="text-right whitespace-nowrap tabular-nums">
        {format.money(row.balance_after)}
        <span className="block text-label text-t3">
          {t("statement.frozenAfter", { amount: format.money(row.frozen_after) })}
        </span>
      </Td>
      <Td className="hidden lg:table-cell">
        <Reference row={row} />
      </Td>
    </tr>
  );
});

// A movement's reference is the call it charged, or the batch it came from: a charge opens the call, which says what the
// money bought, and the rest are named in full on hover.
function Reference({ row }: { row: Schemas["StatementRow"] }) {
  const { t } = useTranslation();
  const short = `${row.reference.slice(0, 8)}…`;
  if (row.kind === "spend" || row.kind === "reversal") {
    return (
      <Link
        to="/calls/$callId"
        params={{ callId: row.reference }}
        title={row.reference}
        className="font-mono text-label text-t3 underline-offset-4 hover:text-t1 hover:underline"
      >
        {t("statement.call", { id: short })}
      </Link>
    );
  }
  return (
    <span className="font-mono text-label text-t4" title={row.reference}>
      {short}
    </span>
  );
}
