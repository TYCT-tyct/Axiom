import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

for (const group of ["tests", "web"]) {
  test(`retries a cold ${group} pull and loads the archive without pulling on a fresh runner`, (t) => {
    const dir = mkdtempSync(join(tmpdir(), "axiom-image-cache-"));
    t.after(() => rmSync(dir, { recursive: true, force: true }));
    const docker = join(dir, "docker");
    writeFileSync(
      docker,
      `#!/bin/bash
set -eu
case "$2" in
  inspect) shift 2; for image; do grep -Fxq -- "$image" "$RUNNER_TEMP/images" || exit 1; done ;;
  pull)
    if [ ! -e "$RUNNER_TEMP/retried" ]; then touch "$RUNNER_TEMP/retried"; exit 1; fi
    echo "$3" >> "$RUNNER_TEMP/images" ;;
  tag) echo "$4" >> "$RUNNER_TEMP/images" ;;
  save) shift 3; archive=$1; shift; printf '%s\\n' "$@" > "$archive" ;;
  load) cp "$4" "$RUNNER_TEMP/images" ;;
esac
`,
      { mode: 0o755 },
    );
    const run = (runner, command) =>
      execFileSync("bash", ["scripts/ci/cache-images.sh", command, group], {
        encoding: "utf8",
        env: {
          ...process.env,
          PATH: `${dir}:${process.env.PATH}`,
          RUNNER_TEMP: runner,
          GITHUB_OUTPUT: join(runner, "output"),
          GITHUB_ENV: join(runner, "env"),
          WEB_IMAGE: "mcr.microsoft.com/playwright:pinned@sha256:012345",
        },
      });
    const cold = join(dir, "cold");
    const warm = join(dir, "warm");
    for (const runner of [cold, warm]) {
      mkdirSync(runner);
      writeFileSync(join(runner, "images"), "");
      run(runner, "key");
    }
    const coldLog = run(cold, "prepare");
    assert.match(coldLog, /Retrying pull of /);
    const archive = `axiom-${group}-images.tar`;
    copyFileSync(join(cold, archive), join(warm, archive));
    const warmLog = run(warm, "prepare");
    assert.match(warmLog, /Loaded cached /);
    assert.doesNotMatch(warmLog, /Pulling|Retrying/);
    const key = (runner) => readFileSync(join(runner, "output"), "utf8").split("\n")[0];
    assert.equal(key(cold), key(warm));
    if (group === "web") {
      assert.equal(readFileSync(join(warm, "env"), "utf8"), "AXIOM_CACHED_WEB_IMAGE=axiom-ci-playwright:012345\n");
    }
    t.diagnostic(coldLog.trim());
    t.diagnostic(warmLog.trim());
  });
}
