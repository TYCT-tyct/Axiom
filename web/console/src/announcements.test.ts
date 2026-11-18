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
    expect(html).toContain("[overflow-wrap:anywhere]");
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

  it("shows a permission state instead of cached announcements after access is refused", () => {
    const html = render({ data: [{ title: "Cached announcement" }] }, new ApiError(403, "request"));
    expect(html).toContain("state.forbiddenTitle");
    expect(html).not.toContain("Cached announcement");
    expect(html).not.toContain("announcements.retry");
  });
});
