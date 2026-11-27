import { describe, expect, it } from "vitest";
import { readCreate, readUpdate, usage } from "./read";

const t = ((key: string) => key) as never;
const proxy = (id: string, fallback_id?: string) =>
  ({ id, name: id, scheme: "http", host: "h", port: 1, has_password: false, status: "active", fallback_id }) as const;

describe("readCreate", () => {
  const ok = { id: "hk-1", name: "HK", scheme: "socks5", host: "10.0.0.1", port: "1080", status: "active" };
  it.each([
    ["an id the contract's pattern refuses", { id: "HK" }, "egress.invalidId"],
    ["the id of the direct exit", { id: "direct" }, "egress.invalidId"],
    ["a host with a scheme in it", { host: "http://a" }, "egress.invalidHost"],
    ["a port out of range", { port: "70000" }, "egress.invalidPort"],
    ["a password with no username", { password: "x" }, "egress.invalidAuth"],
  ])("refuses %s", (_, change, problem) => {
    expect(readCreate({ ...ok, ...change }, t)).toEqual({ problem });
  });

  it("sends a login only when one is typed", () => {
    expect(readCreate(ok, t)).toMatchObject({ port: 1080, auth: undefined, fallback_id: undefined });
    expect(readCreate({ ...ok, username: "u", password: "p" }, t)).toMatchObject({
      auth: { username: "u", password: "p" },
    });
  });
});

describe("readUpdate", () => {
  it("clears the fallback with an explicit null and keeps the login when none is typed", () => {
    expect(readUpdate({ name: "HK", status: "disabled", fallback_id: "" }, t)).toEqual({
      name: "HK",
      status: "disabled",
      fallback_id: null,
      auth: undefined,
    });
  });
});

describe("usage", () => {
  it("counts the credentials that leave by a proxy and the proxies that fall back to it", () => {
    const proxies = [proxy("a"), proxy("b", "a")];
    expect(usage(proxies[0] as never, proxies as never, ["a", "a", "b"])).toEqual({ credentials: 2, fallbackOf: 1 });
  });
});
