import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { runInNewContext } from "node:vm";

const setup = readFileSync(".github/actions/setup/action.yml", "utf8").split(/^    - /m).slice(1);
const workflow = readFileSync(".github/workflows/ci.yml", "utf8");

function field(step, name) {
  return step.match(new RegExp(`^ *${name}: (.+)$`, "m"))?.[1];
}

// The cache conditions use comparisons and boolean operators shared by Actions and JavaScript.
function evaluate(expression, environment, cacheHit = "") {
  return runInNewContext(expression.replace(/\.([\w]+(?:-[\w]+)+)/g, "['$1']"), {
    runner: { environment },
    inputs: { node: "true", images: "tests web", "warm-only": "false" },
    steps: {
      "go-modules": { outputs: { "cache-hit": cacheHit } },
      fonts: { outputs: { "cache-hit": cacheHit } },
    },
    contains: (value, item) => value.includes(item),
  });
}

test("only hosted runners use the Actions download caches", () => {
  const cacheSteps = setup.filter((step) => {
    const uses = field(step, "uses") ?? "";
    return (
      uses.startsWith("actions/cache") ||
      uses.startsWith("./.github/actions/setup/") ||
      ["go-path", "font-key"].includes(field(step, "id")) ||
      ["Download Go modules", "Prepare hosted content cache", "Verify pinned source fonts"].includes(field(step, "name"))
    );
  });
  const warmer = workflow.match(/^  download-cache:\n((?: {4}.*\n|\n)+)/m);
  assert.ok(warmer);
  const warmSteps = warmer[1].split(/^      - /m).slice(1);
  assert.ok(warmSteps.some((step) => field(step, "uses") === "./.github/actions/setup"));
  for (const step of [...cacheSteps, ...warmSteps]) {
    const condition = field(step, "if") ?? "true";
    const label = field(step, "name") ?? field(step, "id") ?? field(step, "uses");
    assert.equal(evaluate(condition, "self-hosted"), false, label);
    assert.equal(evaluate(condition, "github-hosted"), true, label);
  }

  // The image composite is reached only through the two setup steps checked above.
  const imageSteps = cacheSteps.filter((step) => field(step, "uses") === "./.github/actions/setup/images");
  assert.deepEqual(imageSteps.map((step) => field(step, "group")).sort(), ["tests", "web"]);
  assert.doesNotMatch(workflow, /uses: \.\/\.github\/actions\/setup\/images\b/);

  const goSetup = setup.find((step) => field(step, "uses")?.startsWith("actions/setup-go@"));
  assert.equal(evaluate(field(goSetup, "if") ?? "true", "self-hosted"), true);
  assert.equal(field(goSetup, "cache"), "false");
  const nodeSetup = setup.find((step) => field(step, "uses")?.startsWith("actions/setup-node@"));
  assert.equal(evaluate(field(nodeSetup, "if") ?? "true", "self-hosted"), true);
  const pnpmCache = field(nodeSetup, "cache").match(/^\$\{\{ (.+) \}\}$/)[1];
  assert.equal(evaluate(pnpmCache, "self-hosted"), "");
  assert.equal(evaluate(pnpmCache, "github-hosted"), "pnpm");
});

test("the Go module download step runs only on a cache miss", () => {
  const step = setup.find((item) => field(item, "name") === "Download Go modules");
  const condition = field(step, "if");
  assert.equal(evaluate(condition, "github-hosted"), true);
  assert.equal(evaluate(condition, "github-hosted", "false"), true);
  assert.equal(evaluate(condition, "github-hosted", "true"), false);
});

for (const { name, rootFailures, toolsFailures, calls, status } of [
  { name: "both retries succeed", rootFailures: 1, toolsFailures: 1, calls: "root\nroot\ntools\ntools\n", status: 0 },
  { name: "root retry fails", rootFailures: 2, toolsFailures: 0, calls: "root\nroot\n", status: 1 },
  { name: "tools retry fails", rootFailures: 0, toolsFailures: 2, calls: "root\ntools\ntools\n", status: 1 },
]) {
  test(`retries each failed Go module download once on a miss when ${name}`, (t) => {
    const step = setup.find((item) => field(item, "name") === "Download Go modules");
    const script = step.match(/^      run: \|\n((?: {8}.*\n)+)/m)[1];
    const dir = mkdtempSync(join(tmpdir(), "axiom-go-download-"));
    t.after(() => rmSync(dir, { recursive: true, force: true }));
    const root = join(dir, "root");
    mkdirSync(join(root, "tools"), { recursive: true });
    writeFileSync(
      join(dir, "go"),
      `#!/bin/bash
set -eu
test "$*" = "mod download"
module=\${PWD##*/}
echo "$module" >> "$CALLS"
case "$module" in
  root) failures=$ROOT_FAILURES ;;
  tools) failures=$TOOLS_FAILURES ;;
esac
(( $(grep -cx "$module" "$CALLS") > failures ))
`,
      { mode: 0o755 },
    );
    const log = join(dir, "calls");
    const result = spawnSync("bash", ["--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script], {
      cwd: root,
      encoding: "utf8",
      timeout: 5000,
      env: {
        ...process.env,
        PATH: `${dir}:${process.env.PATH}`,
        CALLS: log,
        ROOT_FAILURES: rootFailures,
        TOOLS_FAILURES: toolsFailures,
      },
    });
    assert.ifError(result.error);
    assert.equal(result.status, status, result.stderr);
    assert.equal(readFileSync(log, "utf8"), calls);
  });
}
