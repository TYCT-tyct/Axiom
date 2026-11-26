import { ApiError } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { Announcements } from "./announcements";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

function render(data?: object, error?: Error) {
  const client = new QueryClient();
  const key = [accountKey, "announcements"];
  if (data) client.setQueryData(key, data);
  if (error) client.getQueryCache().build(client, { queryKey: key }).setState({ status: "error", error });
  const markup = renderToStaticMarkup(createElement(QueryClientProvider, { client }, createElement(Announcements)));
  client.clear();
  return markup;
}

describe("site announcement entry", () => {
  it("uses a native keyboard entry and treats administrator text as text", () => {
    const title = "维护🛠".repeat(60);
    const html = render({ data: [{ id: "notice", title, body: '<script>alert("x")</script>' }] });
    expect(html).toContain("<details ");
    expect(html).toContain("<summary>announcements.title</summary>");
    expect(html).toContain(title);
    expect(html).toContain("&lt;script&gt;");
    expect(html).not.toContain("<script>");
    expect(html).toMatch(/<article class="[^"]*root[^"]*"><h2>(维护🛠)+<\/h2><p[^>]*>&lt;script&gt;/);
  });

  it.each([
    [{ data: [] }, "announcements.empty"],
    [{}, "announcements.unavailable"],
    [{ data: [{ id: "old" }] }, "announcements.unavailable"],
  ])("shows an explicit empty or rolling-node state", (data, message) => {
    expect(render(data)).toContain(message);
  });

  it.each([
    [new ApiError(404, "request"), "announcements.unavailable"],
    [new Error("offline"), "announcements.error"],
  ])("provides a retry after a request failure", (error, message) => {
    const html = render(undefined, error);
    expect(html).toContain('role="alert"');
    expect(html).toContain(message);
    expect(html).toContain("announcements.retry");
  });

  it("marks the announcements that began after the newest one this browser opened", () => {
    const data = {
      data: [
        { id: "a", title: "A", starts_at: "2026-11-20T00:00:00Z" },
        { id: "b", title: "B", starts_at: "2026-11-25T00:00:00Z" },
      ],
    };
    expect(render(data)).toContain("announcements.unread");
    const stored = new Map([["axiom.announcements.seen", "2026-11-25T00:00:00.000Z"]]);
    vi.stubGlobal("localStorage", { getItem: (key: string) => stored.get(key) ?? null, setItem: () => {} });
    try {
      expect(render(data)).not.toContain("announcements.unread");
      stored.set("axiom.announcements.seen", "2026-11-21T00:00:00+08:00");
      expect(render(data)).toContain("announcements.unread");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("shows a permission state instead of cached announcements after access is refused", () => {
    const html = render({ data: [{ title: "Cached announcement" }] }, new ApiError(403, "request"));
    expect(html).toContain("state.forbiddenTitle");
    expect(html).not.toContain("Cached announcement");
    expect(html).not.toContain("announcements.retry");
  });
});
