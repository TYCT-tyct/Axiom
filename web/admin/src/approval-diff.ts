import type { AdminSchemas } from "@axiom/api";
import type { DiffRow, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { capOf, envelopeOf, priceRows } from "./approvals";
import { isPerUse, perMillion, rateItems } from "./models/catalog";

type Approval = AdminSchemas["OperationsApproval"];
type Format = ReturnType<typeof useFormat>;

// What an approval changes, row by row, from what the server read when the agent asked (summary), never from the agent's
// words: each tool says its own rows. An approval the page has no rows for (a tool of a later release) has none, and the
// card then shows its reason and limits alone.
const text = (value: unknown) => (typeof value === "string" && value !== "" ? value : undefined);
const summaryOf = (approval: Approval) => (approval.summary ?? {}) as Record<string, unknown>;

const money = (format: Format, amount: unknown, currency: unknown) =>
  typeof amount === "string" && /^-?\d+$/.test(amount) && typeof currency === "string"
    ? format.money({ amount, currency })
    : undefined;

function priceDiff(t: TFunction, format: Format, approval: Approval): DiffRow[] {
  const currency = text(summaryOf(approval).currency) ?? "";
  const show = (item: (typeof rateItems)[number], price: string | undefined) =>
    price === undefined
      ? undefined
      : `${format.decimal(isPerUse(item) ? price : perMillion(price))} ${currency}`.trim();
  return (priceRows(approval.summary, rateItems) ?? []).map((row) => ({
    label: `${t(`models.item.${row.item}`)} · ${t(`models.prices.unit.${isPerUse(row.item) ? "use" : "million"}`)}`,
    before: show(row.item, row.current),
    after: show(row.item, row.proposed) ?? t("approvals.board.diff.noPrice"),
  }));
}

function grantDiff(t: TFunction, format: Format, approval: Approval): DiffRow[] {
  const summary = summaryOf(approval);
  const expires = text(summary.expires_at);
  return [
    { label: t("approvals.board.diff.amount"), after: money(format, summary.amount, summary.currency) ?? "—" },
    {
      label: t("approvals.board.diff.expires"),
      after: expires ? format.dateTimeMinute(new Date(expires)) : t("approvals.request.never"),
    },
  ];
}

function topUpDiff(t: TFunction, format: Format, approval: Approval): DiffRow[] {
  const summary = summaryOf(approval);
  const native = text(summary.amount_native);
  return [
    {
      label: t("approvals.board.diff.native"),
      after: native ? `${format.decimal(native)} ${text(summary.native_unit) ?? ""}`.trim() : "—",
    },
    { label: t("approvals.board.diff.worth"), after: money(format, summary.amount, summary.currency) ?? "—" },
  ];
}

function statusDiff(t: TFunction, approval: Approval): DiffRow[] {
  const now = text(summaryOf(approval).status);
  const next = text((approval.params as Record<string, unknown> | undefined)?.status);
  const name = (status: string | undefined) =>
    status === "listed" || status === "retired" || status === "draft" ? t(`models.state.${status}`) : status;
  return [{ label: t("approvals.board.diff.status"), before: name(now), after: name(next) ?? "—" }];
}

function ruleDiff(t: TFunction, approval: Approval): DiffRow[] {
  const total = summaryOf(approval).moves_total;
  return [
    {
      label: t("approvals.board.diff.rule"),
      after: t(
        approval.tool === "pricing_rule_create" ? "approvals.board.diff.ruleNew" : "approvals.board.diff.ruleEdit",
      ),
    },
    ...(typeof total === "number"
      ? [{ label: t("approvals.board.diff.moves"), after: t("approvals.board.diff.movesCount", { count: total }) }]
      : []),
  ];
}

function refundDiff(t: TFunction, format: Format, approval: Approval): DiffRow[] {
  const summary = summaryOf(approval);
  const credits = money(format, summary.credits, summary.site_currency);
  const cash = money(format, summary.cash, summary.pay_currency);
  return [
    ...(credits ? [{ label: t("approvals.board.diff.credits"), after: credits }] : []),
    ...(cash ? [{ label: t("approvals.board.diff.cash"), after: cash }] : []),
  ];
}

export function diffOf(t: TFunction, format: Format, approval: Approval): DiffRow[] {
  switch (approval.risk) {
    case "gift":
      return grantDiff(t, format, approval);
    case "top_up":
      return topUpDiff(t, format, approval);
    case "price":
      return approval.tool === "price_publish" ? priceDiff(t, format, approval) : ruleDiff(t, approval);
    case "delisting":
      return statusDiff(t, approval);
    case "refund":
      return refundDiff(t, format, approval);
    default:
      return [];
  }
}

// How far what the approval moves is from the limits it was weighed against, in a line: a transfer's day so far against
// the day's limit, a sale price's day of changes against the day's. Nothing for an approval that moves no money and no
// price, or whose summary kept no limits.
export function limitLine(t: TFunction, format: Format, approval: Approval): string | undefined {
  const cap = capOf(approval.summary);
  if (cap) {
    return t("approvals.board.limit.daily", {
      used: format.money(cap.daily.used),
      max: format.money(cap.daily.max),
      amount: format.money(cap.amount),
    });
  }
  const envelope = envelopeOf(approval.summary);
  return envelope
    ? t("approvals.board.limit.changes", { used: envelope.changes.used, max: envelope.changes.max })
    : undefined;
}
