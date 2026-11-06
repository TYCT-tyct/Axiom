import type { AdminSchemas } from "@axiom/api";
import { Panel, Table, Td, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { percentOf } from "./approval-parts";
import { capOf, capOver, envelopeOf, envelopeOver, type PriceRow, priceRows } from "./approvals";
import { formatJSON } from "./lossless";
import { isPerUse, rateItems } from "./models/catalog";
import { showRate } from "./models/price-fields";
import { Facts, Head } from "./parts";

type Approval = AdminSchemas["OperationsApproval"];
type Format = ReturnType<typeof useFormat>;
type Item = (typeof rateItems)[number];

const field = (summary: unknown, key: string) => {
  const value = (summary as Record<string, unknown> | null | undefined)?.[key];
  return typeof value === "string" && value !== "" ? value : undefined;
};

// WhyAsked says in a line each why this approval needed a person: the autonomous limits a transfer goes over, what the
// price envelope does not cover, or the rule that the kind always asks.
export function WhyAsked({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const format = useFormat();
  const reasons = reasonsOf(t, format, approval);
  if (reasons.length === 0) {
    return null;
  }
  return (
    <Panel title={t("approvals.why.title")}>
      <ul className="list-disc space-y-1 py-3 pr-4 pl-9 text-t1">
        {reasons.map((reason) => (
          <li key={reason}>{reason}</li>
        ))}
      </ul>
    </Panel>
  );
}

function reasonsOf(t: TFunction, format: Format, approval: Approval): string[] {
  const cap = capOf(approval.summary);
  const envelope = envelopeOf(approval.summary);
  const reasons: string[] = [];
  if (cap) {
    const limits = {
      single: cap.single.max,
      daily: cap.daily.max,
      weekly: cap.weekly.max,
      objectDaily: cap.objectDaily.max,
    };
    for (const limit of capOver(cap)) {
      reasons.push(
        limit === "count"
          ? t("approvals.why.count", { max: format.number(cap.count.max) })
          : t(`approvals.why.${limit}`, { max: format.money(limits[limit]) }),
      );
    }
    if (cap.paused) {
      reasons.push(t("approvals.why.paused"));
    }
  }
  if (envelope) {
    const percent = percentOf(format.decimal);
    for (const limit of envelopeOver(envelope)) {
      reasons.push(
        t(`approvals.why.envelope.${limit}`, {
          min: percent(envelope.minMargin),
          max: percent(envelope.modelChange.max),
          changes: format.number(envelope.changes.max),
          models: format.number(envelope.models.max),
        }),
      );
    }
  }
  if (approval.risk === "gift" || approval.risk === "delisting") {
    reasons.push(t(`approvals.why.always.${approval.risk}`));
  }
  return reasons;
}

type Fact = [string, ReactNode] | undefined;
type FactOf = (t: TFunction, format: Format, approval: Approval) => Fact;

// Each fact the page formats from a summary, when the summary has it; a key the page does not know stays in the raw
// data only.
const facts: FactOf[] = [
  (t, format, { summary }) => {
    const native = field(summary, "amount_native");
    return native
      ? [t("approvals.request.native"), `${format.decimal(native)} ${field(summary, "native_unit") ?? ""}`.trim()]
      : undefined;
  },
  (t, format, { summary }) => {
    const value = field(summary, "unit_value");
    return value ? [t("approvals.request.unitValue"), format.decimal(value)] : undefined;
  },
  (t, format, { summary }) => {
    const amount = field(summary, "amount");
    const currency = field(summary, "currency");
    return amount && /^\d+$/.test(amount) && currency
      ? [t("approvals.request.amount"), format.money({ amount, currency })]
      : undefined;
  },
  (t, format, { risk, summary }) => {
    if (risk !== "gift") {
      return undefined;
    }
    const expires = field(summary, "expires_at");
    return [
      t("approvals.request.expires"),
      expires ? format.dateTimeMinute(new Date(expires)) : t("approvals.request.never"),
    ];
  },
  (t, _, { risk, summary }) => {
    const status = field(summary, "status");
    return risk === "delisting" && (status === "listed" || status === "retired" || status === "draft")
      ? [t("approvals.request.status"), t(`models.state.${status}`)]
      : undefined;
  },
  (t, _, { summary }) => {
    const audience = (summary as { audience?: { kind?: string } } | undefined)?.audience?.kind;
    return audience === "all" || audience === "tier" || audience === "account"
      ? [t("approvals.request.audience"), t(`pricing.audience.${audience}`)]
      : undefined;
  },
];

// RequestFacts is what the agent asked for, read by the server and formatted: amounts in their currency, a model's
// state, a price's audience.
export function RequestFacts({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const format = useFormat();
  const rows = facts.map((fact) => fact(t, format, approval)).filter((row) => row !== undefined);
  return rows.length === 0 ? null : <Facts title={t("approvals.request.title")} rows={rows} />;
}

const priceColumns = ["current", "proposed", "cost"] as const;
type PriceView = { rows: PriceRow<Item>[]; title: string; show: (item: Item, price: string | undefined) => ReactNode };

// PriceTable is each price a publication changes, now and as proposed, per unit as the catalog shows it, with what it
// costs when the server knew; a price the owner approves is read here, not as a percentage.
export function PriceTable({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const { decimal } = useFormat();
  const rows = priceRows(approval.summary, rateItems);
  if (!rows) {
    return null;
  }
  const currency = field(approval.summary, "currency") ?? "";
  // Every value carries its currency and unit, so one read on its own, as on a phone, is never ambiguous.
  const show = (item: Item, price: string | undefined) =>
    price === undefined ? (
      "—"
    ) : (
      <>
        {decimal(showRate(item, price))} {currency}
        <span className="ml-1 text-label text-t3">{t(`models.prices.unit.${isPerUse(item) ? "use" : "million"}`)}</span>
      </>
    );
  const title = t("approvals.prices.title", { currency });
  return (
    <Panel title={title}>
      <p className="px-4 pt-4 text-t2">{t("approvals.prices.intro")}</p>
      {(approval.summary as { per_endpoint?: boolean }).per_endpoint ? (
        <p className="px-4 pt-2 text-label text-t3">{t("approvals.prices.perEndpoint")}</p>
      ) : null}
      {rows.length === 0 ? (
        <p className="p-4 text-t3">{t("approvals.prices.none")}</p>
      ) : (
        <>
          <PriceBlocks rows={rows} title={title} show={show} />
          <PriceRows rows={rows} title={title} show={show} />
        </>
      )}
    </Panel>
  );
}

// Below sm a table of four columns cannot show its numbers, so each item is a block of its three values.
function PriceBlocks({ rows, show }: PriceView) {
  const { t } = useTranslation();
  return (
    <ul className="divide-y divide-b1 sm:hidden">
      {rows.map((row) => (
        <li key={row.item} className="px-4 py-3">
          <p className="font-medium text-t1">{t(`models.item.${row.item}`)}</p>
          <dl className="mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 tabular-nums">
            {priceColumns.map((column) => (
              <div key={column} className="contents">
                <dt className="text-label text-t3">{t(`approvals.prices.${column}`)}</dt>
                <dd className="text-t1 [overflow-wrap:anywhere]">{show(row.item, row[column])}</dd>
              </div>
            ))}
          </dl>
        </li>
      ))}
    </ul>
  );
}

function PriceRows({ rows, title, show }: PriceView) {
  const { t } = useTranslation();
  return (
    <div className="hidden sm:block">
      <Table label={title}>
        <Head
          cols={[
            [t("approvals.prices.item")],
            ...priceColumns.map((column): [string, string] => [t(`approvals.prices.${column}`), "text-right"]),
          ]}
        />
        <tbody>
          {rows.map((row) => (
            <tr key={row.item}>
              <Td>{t(`models.item.${row.item}`)}</Td>
              {priceColumns.map((column) => (
                <Td key={column} className="whitespace-nowrap text-right tabular-nums">
                  {show(row.item, row[column])}
                </Td>
              ))}
            </tr>
          ))}
        </tbody>
      </Table>
    </div>
  );
}

// RawData keeps everything the server stored of the approval, closed by default: what the page formats above comes
// from it, and a field a later tool adds is still here. It wraps inside its own box at every width.
export function RawData({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  return (
    <Panel className="p-4">
      <details>
        <summary className="cursor-pointer text-t1">{t("approvals.raw.title")}</summary>
        <div className="mt-3 space-y-3">
          <p className="text-label text-t3">{t("approvals.raw.hint")}</p>
          {[
            [t("approvals.raw.summary"), approval.summary],
            [t("approvals.raw.params"), approval.params],
          ].map(([label, value]) => (
            <section key={label as string} className="space-y-1">
              <h3 className="text-label text-t2">{label as string}</h3>
              <pre className="max-h-96 overflow-y-auto whitespace-pre-wrap rounded-md bg-s2 p-3 font-mono text-label text-t2 [overflow-wrap:anywhere]">
                {formatJSON(value)}
              </pre>
            </section>
          ))}
        </div>
      </details>
    </Panel>
  );
}
