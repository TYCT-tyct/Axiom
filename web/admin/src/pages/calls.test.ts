import { describe, expect, it } from "vitest";
import { anyUnfiltered, unfiltered } from "./calls";

// Pages as the call list receives them: an echo of a filter is present only when the node applied it. A page of an older
// node can answer in the middle of a mixed-rollout list, so the page looks at every loaded page, not only the first.
const page = (filters: { model_filter?: string; status_filter?: "error" | "ok"; site_filter?: string }) => ({
  model_filter: filters.model_filter,
  status_filter: filters.status_filter,
  site_filter: filters.site_filter,
});

describe("calls filter echo", () => {
  it("accepts a page that echoed every filter", () => {
    const filters = { model: "model-a", status: "error" as const, site_id: "site_1" };
    expect(unfiltered(filters, page({ model_filter: "model-a", status_filter: "error", site_filter: "site_1" }))).toBe(
      false,
    );
  });

  it("finds the missing site echo", () => {
    expect(unfiltered({ site_id: "site_1" }, page({}))).toBe(true);
  });

  it("flags a later page that an older node did not filter", () => {
    const filters = { model: "model-a", status: "error" as const };
    const pages = [
      page({ model_filter: "model-a", status_filter: "error" }),
      page({ model_filter: "model-a", status_filter: "error" }),
      page({}),
    ];
    expect(anyUnfiltered(filters, pages)).toBe(true);
  });

  it("does not flag a list with no filters", () => {
    expect(anyUnfiltered({}, [page({}), page({})])).toBe(false);
  });
});
