import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

// What the node of cmd/axiom/journey_test.go tells the journey (journeyState there).
export type Node = {
  console: string;
  api: string;
  pay_host: string;
  invite_code: string;
  settle: string;
  calls: string;
};

export const stateVar = "AXIOM_JOURNEY_STATE";

const root = resolve(import.meta.dirname, "..", "..");
const startupLimit = 180_000; // the first run pulls the PostgreSQL and Valkey images and compiles the node

export function readNode(): Node {
  return JSON.parse(readFileSync(process.env[stateVar] ?? "", "utf8"));
}

// Starts the node as a test binary of cmd/axiom that serves until its standard input closes (so a crashed run cannot
// leave it behind), and returns the function that stops it.
export default async function start() {
  // Unique per run: a leftover state file of an earlier run on this host would end the wait for the node at once.
  const dir = mkdtempSync(join(tmpdir(), "axiom-journey-"));
  const binary = join(dir, "journey.test");
  execFileSync("go", ["test", "-tags", "journey", "-c", "-o", binary, "./cmd/axiom"], { cwd: root, stdio: "inherit" });
  const state = join(dir, "state.json");
  process.env[stateVar] = state;
  mkdirSync("test-results", { recursive: true });
  const log = createWriteStream(join("test-results", "journey-node.log"));
  const node = spawn(binary, ["-test.run=^TestJourneyServer$", "-test.count=1", "-test.timeout=30m", "-test.v"], {
    cwd: join(root, "cmd", "axiom"),
    env: process.env,
    stdio: ["pipe", "pipe", "pipe"],
  });
  node.stdout.pipe(log, { end: false });
  node.stderr.pipe(log, { end: false });
  const exited = new Promise<number | null>((done) => node.once("exit", done));
  const deadline = Date.now() + startupLimit;
  while (!existsSync(state)) {
    if (node.exitCode !== null || Date.now() > deadline) {
      node.kill();
      rmSync(dir, { recursive: true, force: true });
      throw new Error(`the node did not start; its log is test-results/journey-node.log (exit ${node.exitCode})`);
    }
    await new Promise((done) => setTimeout(done, 200));
  }
  return async () => {
    try {
      await stop(node, exited);
    } finally {
      log.end();
      rmSync(dir, { recursive: true, force: true });
    }
  };
}

async function stop(node: ChildProcess, exited: Promise<number | null>) {
  node.stdin?.end();
  const timer = setTimeout(() => node.kill("SIGKILL"), 60_000); // the node's own shutdown limit is shorter
  const code = await exited;
  clearTimeout(timer);
  if (code !== 0) {
    throw new Error(`the node ended with ${code}; its log is test-results/journey-node.log`);
  }
}
