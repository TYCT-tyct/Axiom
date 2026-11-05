// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import type { UseQueryResult } from "@tanstack/react-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it, vi } from "vitest";
import en from "./locales/en";
import { LimitsState } from "./routing-limits";

const i18n = createInstance();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "admin", resources: { en: { admin: en } } });
});

// A node of a release before the limits answers 404: the card says they are not there yet and asks again on request,
// rather than calling it a missing page.
it("says an older node has no limits and asks again", async () => {
  const refetch = vi.fn();
  const query = { error: new ApiError(404, null), isFetching: false, refetch } as unknown as UseQueryResult<never>;
  const container = document.createElement("div");
  document.body.append(container);
  await act(async () => {
    createRoot(container).render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nextProvider i18n={i18n}>
          <LimitsState query={query} />
        </I18nextProvider>
      </QueryClientProvider>,
    );
  });
  expect(container.textContent).toContain(en.routingPresets.limits.unavailable);
  const retry = [...container.querySelectorAll("button")].find((b) => b.textContent === en.routingPresets.limits.retry);
  await act(async () => retry?.click());
  expect(refetch).toHaveBeenCalledOnce();
});
