import { appendFileSync, readFileSync } from "node:fs";
import { cpus } from "node:os";

function snapshot() {
  const ticks = readFileSync("/proc/stat", "utf8").split("\n")[0].trim().split(/\s+/).slice(1).map(Number);
  // /proc/stat reports USER_HZ (100 on Linux); guest ticks are already included in user/nice.
  return { seconds: Date.now() / 1000, busy: (ticks[0] + ticks[1] + ticks[2] + ticks[5] + ticks[6] + ticks[7]) / 100 };
}

if (process.env.STATE_cpu) {
  const start = JSON.parse(process.env.STATE_cpu);
  const end = snapshot();
  console.log("CI_CPU=" + JSON.stringify({ seconds: end.seconds - start.seconds, cpu_seconds: end.busy - start.busy, cores: cpus().length }));
} else {
  // Shell steps do not receive the cache service credentials supplied to JavaScript actions.
  for (const name of ["ACTIONS_CACHE_URL", "ACTIONS_RESULTS_URL", "ACTIONS_RUNTIME_TOKEN", "ACTIONS_CACHE_SERVICE_V2"]) {
    if (!process.env[name]) continue;
    if (name === "ACTIONS_RUNTIME_TOKEN") console.log(`::add-mask::${process.env[name]}`);
    appendFileSync(process.env.GITHUB_ENV, `${name}=${process.env[name]}\n`);
  }
  appendFileSync(process.env.GITHUB_STATE, `cpu=${JSON.stringify(snapshot())}\n`);
}
