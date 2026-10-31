import type { Schemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import { filtersUnsupported } from "./calls";

const call = (over: Partial<Schemas["Call"]>) =>
  ({
    id: "call_a",
    error_kind: "",
    key_id: "key_a",
    workspace_id: "ws_a",
    ...over,
  }) as Schemas["Call"];

const page = (data: Schemas["Call"][], status_filter?: Schemas["CallStatus"]) =>
  ({ data, next_cursor: null, status_filter }) as Schemas["CallList"];

describe("filtersUnsupported", () => {
  it("leaves the old filters alone", () => {
    expect(filtersUnsupported({ model: "alpha", client_app: "app" }, [page([call({})])])).toBe(false);
  });

  it("uses the status echo even when the rows happen to match", () => {
    expect(filtersUnsupported({ status: "success" }, [page([call({ error_kind: "" })])])).toBe(true);
    expect(filtersUnsupported({ status: "success" }, [page([call({ error_kind: "" })], "success")])).toBe(false);
  });

  it("rejects rows that do not match the status asked for", () => {
    expect(filtersUnsupported({ status: "error" }, [page([call({ error_kind: "" })], "error")])).toBe(true);
    expect(filtersUnsupported({ status: "error" }, [page([call({ error_kind: "upstream" })], "error")])).toBe(false);
  });

  it.each([
    ["key", { key_id: "key_b" }],
    ["workspace", { workspace_id: "ws_b" }],
    ["request", { request_id: "call_b" }],
  ])("rejects a page where the %s filter was ignored", (_, filters) => {
    expect(filtersUnsupported(filters, [page([call({})])])).toBe(true);
    expect(filtersUnsupported(filters, [page([])])).toBe(false);
  });

  it("accepts a page whose rows all match every new filter", () => {
    const filters = { key_id: "key_a", workspace_id: "ws_a", request_id: "call_a", status: "error" } as const;
    expect(filtersUnsupported(filters, [page([call({ error_kind: "boom" })], "error")])).toBe(false);
  });

  it("checks every loaded page", () => {
    const filters = { key_id: "key_a" };
    expect(filtersUnsupported(filters, [page([call({})]), page([call({ key_id: "key_b" })])])).toBe(true);
  });
});
