import type { CDPSession } from "@playwright/test";

// The trace events of one page load, narrowed to what the gate reads (protocol.d.ts's Tracing.dataCollected).
type TraceEvent = {
  ph?: string;
  name?: string;
  pid?: number;
  tid?: number;
  ts?: number;
  tts?: number;
  dur?: number;
  tdur?: number;
  args?: { name?: string };
};

let loggedComplete = false;
let loggedPartial = false;
function logSample(events: TraceEvent[], main: Set<string>, tasks: TraceEvent[], measured: TraceEvent[]) {
  const partial = measured.length !== tasks.length;
  if (measured.length && (partial ? !loggedPartial : !loggedComplete)) {
    if (partial) loggedPartial = true;
    else loggedComplete = true;
    const missing = tasks.filter((event) => event.tdur === undefined);
    const excerpt = [
      ...events.filter((event) => event.name === "thread_name" && event.args?.name === "CrRendererMain"),
      ...measured.slice(0, 2),
      ...missing.slice(0, 2),
      events.find((event) => event.name === "ThreadControllerImpl::RunTask" && !main.has(`${event.pid}:${event.tid}`)),
      events.find(
        (event) =>
          event.name !== "ThreadControllerImpl::RunTask" && main.has(`${event.pid}:${event.tid}`) && event.tdur,
      ),
    ]
      .filter((event) => event !== undefined)
      .map(({ ph, name, pid, tid, ts, tts, dur, tdur, args }) => ({
        ph,
        name,
        pid,
        tid,
        ts,
        tts,
        dur,
        tdur,
        ...(ph === "M" ? { args: { name: args?.name } } : {}),
      }));
    console.log(
      `vitals trace sample: ${JSON.stringify({
        partial,
        excerpt,
        tasks: tasks.length,
        missingWallUs: missing.reduce((sum, event) => sum + (event.dur ?? 0), 0),
      })}`,
    );
  }
}

// mainThreadBusyMs is the page's own work during a load: the sum of the thread durations (`tdur`) of the renderer main
// thread's tasks. `dur` is wall clock and includes the time the thread was taken off the CPU, so a busy host stretches
// it; `tdur` is the time the thread actually ran, so it measures the page instead of the host. The nested timers inside a
// task are not counted again: only the outermost task events are summed.
export function mainThreadBusyMs(events: TraceEvent[]): number {
  const main = new Set(
    events
      .filter((event) => event.ph === "M" && event.name === "thread_name" && event.args?.name === "CrRendererMain")
      .map((event) => `${event.pid}:${event.tid}`),
  );
  const tasks = events.filter(
    (event) =>
      event.ph === "X" && main.has(`${event.pid}:${event.tid}`) && event.name === "ThreadControllerImpl::RunTask",
  );
  const measured = tasks.filter((event) => event.tdur !== undefined);
  logSample(events, main, tasks, measured);
  if (measured.length === 0) {
    const threads = new Map<string, number>();
    for (const event of events) {
      if (event.ph === "X" && event.name === "ThreadControllerImpl::RunTask") {
        const key = `${event.pid}:${event.tid}`;
        threads.set(key, (threads.get(key) ?? 0) + 1);
      }
    }
    throw new Error(
      `the trace has no main-thread task durations: ${JSON.stringify({
        main: [...main],
        tasks: tasks.length,
        withTdur: measured.length,
        threads: [...threads],
      })}`,
    );
  }
  return measured.reduce((total, event) => total + (event.tdur ?? 0), 0) / 1000;
}

// traced runs load under a Chrome trace and returns what it measured. The trace is started before the navigation and
// ended after the page settled; a failure inside load still ends it, so the next test does not inherit it.
export async function traced<T>(session: CDPSession, load: () => Promise<T>): Promise<{ result: T; busyMs: number }> {
  const events: TraceEvent[] = [];
  const collect = ({ value }: { value: TraceEvent[] }) => {
    events.push(...value);
  };
  session.on("Tracing.dataCollected", collect);
  const complete = new Promise<void>((resolve) => session.once("Tracing.tracingComplete", () => resolve()));
  await session.send("Tracing.start", { categories: "toplevel", transferMode: "ReportEvents" });
  let ended = false;
  try {
    const result = await load();
    await session.send("Tracing.end");
    ended = true;
    await complete;
    return { result, busyMs: mainThreadBusyMs(events) };
  } finally {
    if (!ended) {
      await session.send("Tracing.end").catch(() => undefined);
    }
    session.off("Tracing.dataCollected", collect);
  }
}
