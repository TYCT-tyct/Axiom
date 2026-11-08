import type { Schemas } from "@axiom/api";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { accountKey } from "../session";
import { type Invalid, invalidOf, type RulesDraft, rulesOf } from "./rules";

type Check = (rules: Schemas["RoutingRules"]) => Promise<{ unmatched: Schemas["RoutingRuleUnmatched"][] }>;

// How long the rules rest before they are checked, so typing a price asks once and not on every key.
const settleMs = 400;

// useRulesCheck asks the server which rules of the draft keep no endpoint (checkRoutingRules, or the admin's
// checkLayerRoutingRules through check) while the draft is edited, and says the first as the editor marks it. Only the
// answer for the rules as they are now counts: before it arrives, or when the check fails or refuses the rules (the
// save then says why), nothing is marked and a save is not held, since the server takes such rules anyway. scope names
// what else the answer depends on, such as the layer and currency.
export function useRulesCheck(draft: RulesDraft, check: Check, scope: readonly unknown[]): Invalid | null {
  const written = rulesOf(draft);
  const text = "rules" in written ? JSON.stringify(written.rules) : null;
  const [settled, setSettled] = useState(text);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(text), settleMs);
    return () => clearTimeout(timer);
  }, [text]);
  const answer = useQuery({
    queryKey: [accountKey, "routing-rules-check", ...scope, settled],
    queryFn: () => check(JSON.parse(settled ?? "{}")),
    enabled: settled !== null,
    retry: false,
    staleTime: 60_000,
  });
  return answer.data && settled === text ? invalidOf(answer.data) : null;
}
