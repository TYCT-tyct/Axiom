import type { Schemas } from "@axiom/api";
import { Stat } from "@axiom/app";
import { Badge, DataState, PageHeader, Panel, Table, Td, Th, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { balanceQuery } from "../queries";
import { TopUp } from "../top-up";

export default function Balance() {
  const { t } = useTranslation();
  const format = useFormat();
  const balance = useQuery(balanceQuery);
  return (
    <>
      <PageHeader title={t("nav.balance")} description={t("balance.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <DataState query={balance}>
          {(data) => (
            <>
              <TopUp currency={data.balance.currency} />
              <div className="grid gap-4 sm:grid-cols-2">
                <Stat
                  label={t("balance.available")}
                  value={format.money(data.available)}
                  note={t("balance.availableNote")}
                />
                <Stat
                  label={t("balance.spentThisMonth")}
                  value={format.money(data.spent_this_month)}
                  note={t("balance.monthNote")}
                />
              </div>
              <Breakdown data={data} />
              <Panel title={t("balance.batches")}>
                {data.batches === undefined ? (
                  <p className="px-4 py-3 text-t3">{t("balance.batchesUnavailable")}</p>
                ) : data.batches.length === 0 ? (
                  <p className="px-4 py-3 text-t3">{t("balance.noBatches")}</p>
                ) : (
                  <BatchTable batches={data.batches} />
                )}
                <p className="border-t border-b1 px-4 py-3 text-ui text-t3">{t("balance.batchesNote")}</p>
              </Panel>
              <Link to="/statement" className="text-ui text-accent underline-offset-4 hover:underline">
                {t("nav.statement")}
              </Link>
            </>
          )}
        </DataState>
      </div>
    </>
  );
}

// Breakdown is the balance and what is held back from it, each line saying what the figure is.
function Breakdown({ data }: { data: Schemas["Balance"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Panel title={t("balance.breakdown")}>
      <dl className="divide-y divide-b1">
        {(
          [
            ["balance", data.balance],
            ["reserved", data.reserved],
            ["floor", data.floor],
          ] as const
        ).map(([name, money]) => (
          <div key={name} className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1 px-4 py-3">
            <dt className="text-t1">{t(`balance.${name}`)}</dt>
            <dd className="col-start-2 row-span-2 row-start-1 text-right text-t1 tabular-nums [overflow-wrap:anywhere]">
              {format.money(money)}
            </dd>
            <dd className="max-w-prose text-label text-t3">{t(`balance.${name}Note`)}</dd>
          </div>
        ))}
      </dl>
    </Panel>
  );
}

// BatchTable is what is left of each grant or payment and when it stops counting. The expiry is the batch's own, not the
// site's: a batch without one never expires, and an expiry within a week is marked so it is not read past.
function BatchTable({ batches }: { batches: NonNullable<Schemas["Balance"]["batches"]> }) {
  const { t } = useTranslation();
  return (
    <Table label={t("balance.batches")}>
      <thead>
        <tr>
          <Th>{t("balance.col.batch")}</Th>
          <Th>{t("balance.col.source")}</Th>
          <Th className="text-right">{t("balance.col.amount")}</Th>
          <Th className="text-right">{t("balance.col.remaining")}</Th>
          <Th>{t("balance.col.expires")}</Th>
        </tr>
      </thead>
      <tbody>
        {batches.map((batch) => (
          <BatchRow key={batch.id} batch={batch} />
        ))}
      </tbody>
    </Table>
  );
}

function BatchRow({ batch }: { batch: NonNullable<Schemas["Balance"]["batches"]>[number] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr className="hover:bg-s2">
      <Td>
        <span className="font-mono text-label text-t2" title={batch.id}>
          {batch.id.slice(0, 8)}
          {"…"}
        </span>
        <span className="block text-label text-t3">{format.dateTime(new Date(batch.created_at))}</span>
      </Td>
      <Td>
        <Badge tone={batch.source === "paid" ? "neutral" : "ok"}>{t(`balance.source.${batch.source}`)}</Badge>
      </Td>
      <Td className="text-right whitespace-nowrap tabular-nums">{format.money(batch.amount)}</Td>
      <Td className="text-right whitespace-nowrap tabular-nums">
        {format.money(batch.remaining)}
        {batch.frozen.amount !== "0" ? (
          <span className="block text-label text-t3">
            {t("balance.frozen", { amount: format.money(batch.frozen) })}
          </span>
        ) : null}
      </Td>
      <Td className="whitespace-nowrap">
        <Expiry at={batch.expires_at} />
      </Td>
    </tr>
  );
}

function Expiry({ at }: { at: string | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!at) {
    return <span className="text-t3">{t("balance.neverExpires")}</span>;
  }
  const date = new Date(at);
  const left = date.getTime() - Date.now();
  const expired = left <= 0;
  return (
    <>
      {format.dateTime(date)}
      {left <= 7 * 24 * 60 * 60_000 ? (
        <span className="mt-1 block">
          <Badge tone={expired ? "bad" : "warn"} icon="alert">
            {t(expired ? "balance.expired" : "balance.expiresSoon")}
          </Badge>
        </span>
      ) : null}
    </>
  );
}
