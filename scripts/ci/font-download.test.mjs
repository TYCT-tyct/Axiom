import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { sourceFont, sources } from "../../web/packages/boards/fonts.ts";

test("resumes a disconnected font and reuses its verified cache", async (t) => {
  const cache = await mkdtemp(join(tmpdir(), "axiom-font-download-"));
  t.after(() => rm(cache, { recursive: true, force: true }));
  process.env.XDG_CACHE_HOME = cache;
  const body = Buffer.from("verified font bytes");
  const prefix = 4;
  const requests = [];
  const server = createServer((request, response) => {
    requests.push(request.headers.range);
    if (request.headers.range === `bytes=${prefix}-`) {
      response.writeHead(206, {
        "Content-Length": body.length - prefix,
        "Content-Range": `bytes ${prefix}-${body.length - 1}/${body.length}`,
      });
      response.end(body.subarray(prefix));
    } else {
      response.writeHead(200, { "Content-Length": body.length });
      response.write(body.subarray(0, prefix), () => response.destroy());
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));

  const name = "interrupted-download";
  sources[name] = {
    url: `http://127.0.0.1:${server.address().port}/font`,
    sha256: createHash("sha256").update(body).digest("hex"),
  };
  assert.deepEqual(await sourceFont(name), body);

  const restored = await import("../../web/packages/boards/fonts.ts?restored");
  restored.sources[name] = sources[name];
  assert.deepEqual(await restored.sourceFont(name), body);
  assert.deepEqual(requests, [undefined, `bytes=${prefix}-`]);
});
