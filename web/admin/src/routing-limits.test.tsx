// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import type { UseQueryResult } from "@tanstack/react-query";
import { act, type ComponentType } from "react";
import { expect, it, vi } from "vitest";
import en from "./locales/en";
import { LimitsState } from "./routing-limits";
import { SuspensionState } from "./routing-suspension";
import { renderInDom } from "./test-dom";

// A node of a release before the limits, or before the switch that suspends presets, answers 404: the card says they
// are not there yet and asks again on request, rather than calling it a missing page.
it.each<[string, ComponentType<{ query: UseQueryResult<never> }>, string]>([
  ["limits", LimitsState, en.routingPresets.limits.unavailable],
  ["suspension", SuspensionState, en.routingPresets.suspension.unavailable],
])("says an older node has no %s and asks again", async (_, State, unavailable) => {
  const refetch = vi.fn();
  const query = { error: new ApiError(404, null), isFetching: false, refetch } as unknown as UseQueryResult<never>;
  const { container, unmount } = await renderInDom(<State query={query} />);
  expect(container.textContent).toContain(unavailable);
  const retry = [...container.querySelectorAll("button")].find((b) => b.textContent === en.routingPresets.limits.retry);
  await act(async () => retry?.click());
  expect(refetch).toHaveBeenCalledOnce();
  await unmount();
});
