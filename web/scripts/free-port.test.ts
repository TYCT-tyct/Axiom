import { createServer, type Server } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { classify, freeFirstPort, isFree } from "./free-port.ts";

const failure = (code: string | undefined) => Object.assign(new Error("listen failed"), { code });

describe("classify", () => {
  it.each([
    { code: "EADDRINUSE", want: "taken" },
    { code: "EACCES", want: "taken" }, // a port Windows has reserved
    { code: "EADDRNOTAVAIL", want: "absent" }, // no such address on this machine, such as ::1 without IPv6
    { code: "EAFNOSUPPORT", want: "absent" },
  ])("reads $code as $want", ({ code, want }) => {
    expect(classify(failure(code), "::1", 6173)).toBe(want);
  });

  it.each([{ code: "EPERM" }, { code: "EMFILE" }, { code: undefined }])(
    "throws for $code, naming the address, the port and the cause",
    ({ code }) => {
      const error = failure(code);
      let thrown: unknown;
      try {
        classify(error, "127.0.0.1", 6173);
      } catch (caught) {
        thrown = caught;
      }
      expect(thrown).toBeInstanceOf(Error);
      expect((thrown as Error).message).toContain("127.0.0.1:6173");
      expect((thrown as Error).message).toContain("listen failed");
      expect((thrown as Error).cause).toBe(error);
    },
  );
});

describe("isFree", () => {
  const held: Server[] = [];
  afterEach(() => Promise.all(held.splice(0).map((s) => new Promise((resolve) => s.close(resolve)))));

  const hold = (host: string) =>
    new Promise<number>((resolve, reject) => {
      const server = createServer();
      held.push(server);
      server.once("error", reject);
      server.listen(0, host, () => resolve((server.address() as { port: number }).port));
    });

  it("is false for a port something listens on, whichever address it took", async () => {
    expect(await isFree(await hold("127.0.0.1"))).toBe(false);
  });

  it("is true for a port nothing listens on", async () => {
    const port = await hold("127.0.0.1");
    await Promise.all(held.splice(0).map((s) => new Promise((resolve) => s.close(resolve))));
    expect(await isFree(port)).toBe(true);
  });
});

describe("freeFirstPort", () => {
  it("returns a first port whose three apps and their preview ports can be listened on", async () => {
    const first = await freeFirstPort();
    expect(first).toBeGreaterThanOrEqual(20_000);
    for (const app of [0, 1, 2]) {
      expect(await isFree(first + app)).toBe(true);
      expect(await isFree(first + app + 1000)).toBe(true);
    }
  });
});
