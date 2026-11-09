import type { AdminSchemas } from "@axiom/api";
import { complete, count, record } from "./approvals";

// The parts of an approval only its page reads: the router and the approvals list load approvals.ts with the first
// screen, so what only the approval page needs lives here, in that page's chunk (scripts/budget.ts).

type Approval = AdminSchemas["OperationsApproval"];

// The envelope's own limits, which every summary weighed against it has, a pricing rule change's too: the least margin,
// a model's daily change, and the day's price changes and models.
export type EnvelopeMaxima = { minMargin: number; modelChange: number; changes: number; models: number };

export function envelopeMaximaOf(summary: unknown): EnvelopeMaxima | undefined {
  const e = record(record(summary)?.envelope);
  return (
    e &&
    complete({
      minMargin: count(e.min_margin_bps),
      modelChange: count(e.model_daily_change_bps),
      changes: count(e.daily_changes),
      models: count(e.daily_models),
    })
  );
}

type Rule = AdminSchemas["PricingRuleInput"];

// A sale price a pricing rule change moved (internal/operations shownMoves): the model, whom it is sold to, its move
// and margin in basis points (null when the server could not measure them), and the model's change today before it.
export type RuleMove = {
  model: string;
  slug?: string;
  audience?: Rule["audience"]["kind"];
  change: number | null;
  margin: number | null;
  modelUsed?: number;
};

// A pricing rule change as the server kept it: the rule asked for (params) and, for a change, the revision it was read
// at; every price it moved, at most the first few of total; and the day's price changes and repriced models before it.
export type RuleChange = {
  rule?: Rule;
  // The site's currency, in which a cost-plus rule's fixed markup is stated.
  currency?: string;
  rev?: number;
  moves: RuleMove[];
  total: number;
  day?: { changes: { used: number; max: number }; models: { used: number; max: number } };
};

const audiences = ["all", "tier", "account"] as const;

const ruleOf = (value: unknown): Rule | undefined => {
  const r = record(value);
  const kind = (key: string) => record(r?.[key])?.kind;
  return r &&
    typeof kind("target") === "string" &&
    typeof kind("audience") === "string" &&
    typeof kind("method") === "string"
    ? (r as Rule)
    : undefined;
};

const moveOf = (value: unknown): RuleMove[] => {
  const m = record(value);
  if (typeof m?.model_id !== "string") {
    return [];
  }
  const audience = record(m.audience)?.kind;
  const measured = (v: unknown) => (v === null ? null : (count(v) ?? null));
  return [
    {
      model: m.model_id,
      slug: typeof m.slug === "string" && m.slug !== "" ? m.slug : undefined,
      audience: audiences.find((kind) => kind === audience),
      change: measured(m.change_bps),
      margin: measured(m.margin_bps),
      modelUsed: count(m.model_change_used),
    },
  ];
};

// ruleChangeOf reads a pricing rule write's approval; anything else, or one whose summary lists no moves, has none.
export function ruleChangeOf(approval: Approval): RuleChange | undefined {
  if (approval.tool !== "pricing_rule_create" && approval.tool !== "pricing_rule_update") {
    return undefined;
  }
  const summary = record(approval.summary);
  if (!Array.isArray(summary?.moves)) {
    return undefined;
  }
  const params = record(approval.params);
  const moves = summary.moves.flatMap(moveOf);
  const e = record(summary.envelope);
  const day =
    e &&
    complete({
      changesUsed: count(e.daily_changes_used),
      changesMax: count(e.daily_changes),
      modelsUsed: count(e.daily_models_used),
      modelsMax: count(e.daily_models),
    });
  return {
    rule: ruleOf(params?.rule),
    currency: typeof summary.currency === "string" ? summary.currency : undefined,
    rev: approval.tool === "pricing_rule_update" ? count(params?.rev) : undefined,
    moves,
    total: count(summary.moves_total) ?? moves.length,
    day: day && {
      changes: { used: day.changesUsed, max: day.changesMax },
      models: { used: day.modelsUsed, max: day.modelsMax },
    },
  };
}
