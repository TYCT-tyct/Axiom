// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import type { UseQueryResult } from "@tanstack/react-query";
import { act } from "react";
import { expect, it, vi } from "vitest";
import en from "./locales/en";
import { LimitsState } from "./routing-limits";
import { renderInDom } from "./test-dom";

// A node of a release before the limits answers 404: the card says they are not there yet and asks again on request,
// rather than calling it a missing page.
it("says an older node has no limits and asks again", async () => {
  const refetch = vi.fn();
  const query = { error: new ApiError(404, null), isFetching: false, refetch } as unknown as UseQueryResult<never>;
  const { container, unmount } = await renderInDom(<LimitsState query={query} />);
  expect(container.textContent).toContain(en.routingPresets.limits.unavailable);
  const retry = [...container.querySelectorAll("button")].find((b) => b.textContent === en.routingPresets.limits.retry);
  await act(async () => retry?.click());
  expect(refetch).toHaveBeenCalledOnce();
  await unmount();
});
