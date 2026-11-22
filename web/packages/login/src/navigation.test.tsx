import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { type LoginLinkProps, LoginNavigationProvider } from "./index";
import { RouteNote } from "./login-parts";

function TestLink({ to, search, ...props }: LoginLinkProps) {
  const qs = search?.next ? `?next=${encodeURIComponent(search.next)}` : "";
  return <a href={`/console${to}${qs}`} {...props} />;
}

describe("LoginNavigationProvider", () => {
  it("lets RouteNote render a host-provided anchor without importing a router", () => {
    const html = renderToStaticMarkup(
      <LoginNavigationProvider value={{ search: { next: "/keys" }, navigate: vi.fn(), Link: TestLink }}>
        <RouteNote to="/register" lead="New here?" label="Register" next="/keys" />
      </LoginNavigationProvider>,
    );
    expect(html).toContain('href="/console/register?next=%2Fkeys"');
    expect(html).toContain("New here?");
    expect(html).toContain("Register");
  });
});
