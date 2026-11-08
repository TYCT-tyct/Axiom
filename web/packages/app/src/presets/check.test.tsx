// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { useRulesCheck } from "./check";
import { draftOf, type RulesDraft } from "./rules";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => vi.useRealTimers());

// The server keeps no endpoint for a policy list without may_train, and says so of the default rule.
const check = vi.fn((rules: Schemas["RoutingRules"]) => {
  const policies = (rules as { default?: { policies?: string[] } }).default?.policies;
  return Promise.resolve({
    unmatched: policies && !policies.includes("may_train") ? [{ fields: ["policies" as const] }] : [],
  });
});

function Probe({ draft }: { draft: RulesDraft }) {
  const invalid = useRulesCheck(draft, check, ["test"]);
  return <p>{invalid ? `${invalid.rule}:${invalid.field}` : "none"}</p>;
}

const narrow = draftOf({ default: { policies: ["no_retention"] } } as Schemas["RoutingRules"]);
const open = draftOf({ default: {} } as Schemas["RoutingRules"]);

// The rules are checked once they rest, and the answer marks them only while it is the answer for the rules as they
// are: an edit clears the mark until the edited rules are checked in turn.
it("marks the rules the latest answer is for, once they rest", async () => {
  vi.useFakeTimers();
  const container = document.createElement("div");
  const root = createRoot(container);
  const client = new QueryClient();
  const render = (draft: RulesDraft) =>
    act(async () =>
      root.render(
        <QueryClientProvider client={client}>
          <Probe draft={draft} />
        </QueryClientProvider>,
      ),
    );
  const settle = () => act(async () => vi.advanceTimersByTimeAsync(500));
  await render(narrow);
  expect(container.textContent).toBe("none");
  await settle();
  expect(container.textContent).toBe("default:policies");
  await render(open);
  expect(container.textContent).toBe("none");
  await settle();
  expect(container.textContent).toBe("none");
  expect(check).toHaveBeenCalledTimes(2);
  await act(async () => root.unmount());
});

// An answer that named a rule stops holding the save once asking again fails, as against a node without the check.
it("drops the mark when asking again fails", async () => {
  vi.useFakeTimers();
  const container = document.createElement("div");
  const root = createRoot(container);
  const client = new QueryClient();
  const settle = () => act(async () => vi.advanceTimersByTimeAsync(500));
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <Probe draft={narrow} />
      </QueryClientProvider>,
    ),
  );
  await settle();
  expect(container.textContent).toBe("default:policies");
  check.mockRejectedValueOnce(new Error("404"));
  await act(async () => client.refetchQueries());
  await settle();
  expect(container.textContent).toBe("none");
  await act(async () => root.unmount());
});
