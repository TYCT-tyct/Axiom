import type { AdminSchemas } from "@axiom/api";

type Rules = AdminSchemas["RoutingRules"];

// A platform preset's line order is `advanced.order`: the supplier groups' codenames the router tries first, in this
// order (docs/domains/routing.md「规则内容」). The drawer edits that list and nothing else of the rules: every other
// member, and every other member of `advanced`, is written back as it was.

export function orderOf(rules: Rules): readonly string[] {
  const order = rules.advanced?.order;
  return Array.isArray(order) ? order.filter((code): code is string => typeof code === "string") : [];
}

export function withOrder(rules: Rules, order: readonly string[]): Rules {
  return { ...rules, advanced: { ...rules.advanced, order: [...order] } };
}

// The line at `at` one place up (-1) or down (1); a move off either end leaves the order as it is.
export function move(order: readonly string[], at: number, by: -1 | 1): string[] {
  const to = at + by;
  if (to < 0 || to >= order.length) return [...order];
  return order.map((code, i) => (i === at ? (order[to] as string) : i === to ? (order[at] as string) : code));
}

// A preset keeps at least one line once it has an order: with none, the order would be a different thing (the router's
// own choice), and `no_fallbacks` needs one.
export const remove = (order: readonly string[], at: number): string[] =>
  order.length > 1 ? order.filter((_, i) => i !== at) : [...order];

export const add = (order: readonly string[], code: string): string[] =>
  order.includes(code) ? [...order] : [...order, code];

export const same = (a: readonly string[], b: readonly string[]) =>
  a.length === b.length && a.every((c, i) => c === b[i]);
