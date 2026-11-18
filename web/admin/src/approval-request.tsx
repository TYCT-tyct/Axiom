import type { AdminSchemas } from "@axiom/api";
import { CodeBlock } from "@axiom/boards/ds";
import { Card, Disclosure, List, ListItem, Stack, Table, Td, Text, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { percentOf } from "./approval-parts";
import { envelopeMaximaOf, type RuleChange, type RuleMove, ruleChangeOf } from "./approval-rule";
import {
  type CapLimit,
  capOf,
  type EnvelopeLimit,
  type PriceRow,
  priceRows,
  type Reason,
  reasonsOf,
} from "./approvals";
import { formatJSON } from "./lossless";
import { isPerUse, rateItems } from "./models/catalog";
import { showRate } from "./models/price-fields";
import { Rates } from "./models/prices";
import { Facts, Head } from "./parts";
import { MethodSummary } from "./pricing/describe";

type Approval = AdminSchemas["OperationsApproval"];
type Format = ReturnType<typeof useFormat>;
type Item = (typeof rateItems)[number];

const field = (summary: unknown, key: string) => {
  const value = (summary as Record<string, unknown> | null | undefined)?.[key];
  return typeof value === "string" && value !== "" ? value : undefined;
};

// WhyAsked says in a line each why this approval needed a person, as the server decided it: the autonomous limits a
// transfer goes over, what the price envelope does not cover, or the rule that the kind always asks. The limits come
// from the server's summary; one it did not keep is shown as unknown. An approval an older node asked for has no
// reasons, and the panel says it cannot tell.
export function WhyAsked({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const format = useFormat();
  const reasons = reasonsOf(approval);
  // Two codes a later server added read the same here, so each line is shown once.
  const lines = reasons && [...new Set(reasons.map((reason) => sentenceOf(t, format, approval, reason)))];
  if (lines?.length === 0) {
    return null;
  }
  return (
    <Card title={t("approvals.why.title")}>
      {lines ? (
        <List>
          {lines.map((line) => (
            <ListItem key={line}>{line}</ListItem>
          ))}
        </List>
      ) : (
        <Text tone="faint">{t("approvals.why.unavailable")}</Text>
      )}
    </Card>
  );
}

// unknown stands for a limit the summary did not keep, so a sentence never shows a placeholder.
const unknown = "—";

function sentenceOf(t: TFunction, format: Format, approval: Approval, reason: Reason): string {
  switch (reason.kind) {
    case "always":
      return approval.risk === "gift" || approval.risk === "delisting" || approval.risk === "refund"
        ? t(`approvals.why.always.${approval.risk}`)
        : t("approvals.why.always.other");
    case "paused":
      return t("approvals.why.paused");
    case "cap":
      return capSentence(t, format, approval, reason.limit);
    case "envelope":
      return envelopeSentence(t, format, approval, reason.limit);
    default:
      return t("approvals.why.unknown");
  }
}

function capSentence(t: TFunction, format: Format, approval: Approval, limit: CapLimit): string {
  const cap = capOf(approval.summary);
  if (limit === "count") {
    return t("approvals.why.count", { max: cap ? format.number(cap.count.max) : unknown });
  }
  const max = cap?.[limit].max;
  return t(`approvals.why.${limit}`, { max: max ? format.money(max) : unknown });
}

function envelopeSentence(t: TFunction, format: Format, approval: Approval, limit: EnvelopeLimit): string {
  const envelope = envelopeMaximaOf(approval.summary);
  const percent = percentOf(format.decimal);
  return t(`approvals.why.envelope.${limit}`, {
    min: envelope ? percent(envelope.minMargin) : unknown,
    max: envelope ? percent(envelope.modelChange) : unknown,
    changes: envelope ? format.number(envelope.changes) : unknown,
    models: envelope ? format.number(envelope.models) : unknown,
  });
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
  // A refund's credits, in the site's currency, and the cash it returns, in the currency the order was paid in: the
  // amount after them is what its caps count, converted.
  (t, format, { risk, summary }) => {
    const amount = field(summary, "credits");
    const currency = field(summary, "site_currency");
    return risk === "refund" && amount && /^\d+$/.test(amount) && currency
      ? [t("approvals.request.credits"), format.money({ amount, currency })]
      : undefined;
  },
  (t, format, { risk, summary }) => {
    const amount = field(summary, "cash");
    const currency = field(summary, "pay_currency");
    return risk === "refund" && amount && /^\d+$/.test(amount) && currency
      ? [t("approvals.request.cash"), format.pay({ amount, currency })]
      : undefined;
  },
  (t, format, { risk, summary }) => {
    const amount = field(summary, "amount");
    const currency = field(summary, "currency");
    return amount && /^\d+$/.test(amount) && currency
      ? [
          t(risk === "refund" ? "approvals.request.capAmount" : "approvals.request.amount"),
          format.money({ amount, currency }),
        ]
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
        {decimal(showRate(item, price))} {currency}{" "}
        <Text as="span" size="sm" tone="faint">
          {t(`models.prices.unit.${isPerUse(item) ? "use" : "million"}`)}
        </Text>
      </>
    );
  const title = t("approvals.prices.title", { currency });
  // Equal base prices say nothing of an endpoint's or a tier's own price; only the server knows whether they are all.
  const complete = (approval.summary as { prices_complete?: unknown }).prices_complete === true;
  const status = t(complete ? "approvals.prices.unlisted" : "approvals.prices.partial");
  return (
    <Card title={title} lede={`${t("approvals.prices.intro")} ${status}`} layout="flush">
      {rows.length === 0 ? (
        <Text tone="faint">{t(complete ? "approvals.prices.none" : "approvals.prices.partialNone")}</Text>
      ) : (
        <PriceRows rows={rows} title={title} show={show} />
      )}
    </Card>
  );
}

function PriceRows({ rows, title, show }: PriceView) {
  const { t } = useTranslation();
  return (
    <Table label={title}>
      <Head
        cols={[
          t("approvals.prices.item"),
          ...priceColumns.map((column): [string, { align: "end" }] => [
            t(`approvals.prices.${column}`),
            { align: "end" },
          ]),
        ]}
      />
      <tbody>
        {rows.map((row) => (
          <tr key={row.item}>
            <Td>{t(`models.item.${row.item}`)}</Td>
            {priceColumns.map((column) => (
              <Td key={column} align="end">
                {show(row.item, row[column])}
              </Td>
            ))}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

// RuleChangePanel is a pricing rule change the agent asked for: the rule as asked, and every sale price it moved when
// the agent asked, each against the model's change today, beside the day's use of the envelope. The server keeps the
// first few moves only, and the panel says how many there are.
export function RuleChangePanel({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const change = ruleChangeOf(approval);
  if (!change) {
    return null;
  }
  return (
    <>
      <RuleFacts change={change} />
      <Card title={t("approvals.rule.moves")}>
        <Stack gap={12}>
          <Text tone="muted">{t("approvals.rule.movesIntro")}</Text>
          {change.day ? <DayUse day={change.day} /> : null}
          {change.total > change.moves.length ? (
            <Text tone="warn">
              {t("approvals.rule.truncated", { shown: change.moves.length, total: change.total })}
            </Text>
          ) : null}
          {change.moves.length === 0 ? (
            <Text tone="faint">{t("approvals.rule.none")}</Text>
          ) : (
            <List>
              {change.moves.map((move, i) => (
                <ListItem key={`${move.model}:${i}`}>
                  <MoveLine move={move} />
                </ListItem>
              ))}
            </List>
          )}
        </Stack>
      </Card>
    </>
  );
}

function RuleFacts({ change }: { change: RuleChange }) {
  const { t } = useTranslation();
  const rule = change.rule;
  if (!rule) {
    return null;
  }
  const rows: [string, ReactNode][] = [
    [t("pricing.rules.target"), [t(`pricing.target.${rule.target.kind}`), rule.target.id].filter(Boolean).join(" · ")],
    [
      t("pricing.rules.audience"),
      [t(`pricing.audience.${rule.audience.kind}`), rule.audience.id].filter(Boolean).join(" · "),
    ],
    [t("pricing.rules.method"), <MethodSummary key="method" method={rule.method} />],
    [t("pricing.rules.state"), t(rule.enabled ? "pricing.on" : "pricing.off")],
  ];
  const conversion = rule.method.currency_conversion;
  if (conversion) {
    rows.push([t("pricing.method.conversion"), t(`pricing.method.${conversion}`)]);
  }
  if (change.rev !== undefined) {
    rows.push([t("approvals.rule.rev"), String(change.rev)]);
  }
  const prices = methodPrices(rule.method, change.currency);
  return (
    <>
      <Facts title={t("approvals.rule.title")} rows={rows} />
      {prices ? (
        <Card title={t(prices.title, { currency: prices.schedule.currency })}>
          <Rates schedule={prices.schedule} />
        </Card>
      ) : null}
    </>
  );
}

// methodPrices is every price a rule's method states as an amount, as a price list: a fixed price, or the fixed markup
// a cost-plus rule adds to cost in the site's currency. The owner approves those numbers, not the method's name.
function methodPrices(
  method: AdminSchemas["PricingMethod"],
  currency: string | undefined,
):
  | { title: "approvals.rule.schedule" | "approvals.rule.fixedMarkup"; schedule: AdminSchemas["PriceSchedule"] }
  | undefined {
  if (method.kind === "fixed" && method.schedule) {
    return { title: "approvals.rule.schedule", schedule: method.schedule };
  }
  if (method.fixed_markup && Object.keys(method.fixed_markup).length > 0) {
    return {
      title: "approvals.rule.fixedMarkup",
      schedule: { v: 1, currency: currency ?? "", rates: method.fixed_markup },
    };
  }
  return undefined;
}

function DayUse({ day }: { day: NonNullable<RuleChange["day"]> }) {
  const { t } = useTranslation();
  const { number } = useFormat();
  return (
    <Text tone="muted" numeric>
      {t("approvals.rule.day", {
        changes: number(day.changes.used),
        changesMax: number(day.changes.max),
        models: number(day.models.used),
        modelsMax: number(day.models.max),
      })}
    </Text>
  );
}

// MoveLine is one moved price on its own lines, which wrap at any width.
function MoveLine({ move }: { move: RuleMove }) {
  const { t } = useTranslation();
  const format = useFormat();
  const percent = percentOf(format.decimal);
  return (
    <Stack gap={4}>
      <Text medium>
        {move.slug ?? move.model}
        {move.audience ? (
          <>
            {" "}
            <Text as="span" size="sm" tone="faint">
              {t(`pricing.audience.${move.audience}`)}
            </Text>
          </>
        ) : null}
      </Text>
      <Text tone="muted" numeric>
        {t("approvals.rule.move", {
          change: move.change === null ? t("approvals.envelope.unmeasured") : percent(move.change),
          margin: move.margin === null ? t("approvals.envelope.unknownCost") : percent(move.margin),
          used: move.modelUsed === undefined ? unknown : percent(move.modelUsed),
        })}
      </Text>
    </Stack>
  );
}

// RawData keeps everything the server stored of the approval, closed by default: what the page formats above comes
// from it, and a field a later tool adds is still here. It wraps inside its own box at every width.
export function RawData({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  return (
    <Card>
      <Disclosure summary={t("approvals.raw.title")}>
        <Stack gap={12}>
          <Text size="sm" tone="muted">
            {t("approvals.raw.hint")}
          </Text>
          {[
            [t("approvals.raw.summary"), approval.summary],
            [t("approvals.raw.params"), approval.params],
            // A reason this page cannot name is read here, as the server sent it.
            ...(approval.reasons ? [[t("approvals.raw.reasons"), approval.reasons]] : []),
          ].map(([label, value]) => (
            <Stack key={label as string} gap={4}>
              <Text as="h3" size="sm" tone="muted">
                {label as string}
              </Text>
              <CodeBlock label={label as string}>{formatJSON(value)}</CodeBlock>
            </Stack>
          ))}
        </Stack>
      </Disclosure>
    </Card>
  );
}
