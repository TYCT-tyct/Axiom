import type { CDPSession } from "@playwright/test";

// The trace events of one page load, narrowed to what the gate reads (protocol.d.ts's Tracing.dataCollected).
type TraceEvent = {
  ph?: string;
  name?: string;
  pid?: number;
  tid?: number;
  tdur?: number;
  args?: { name?: string };
};

// mainThreadBusyMs is the page's own work during a load: the sum of the thread durations (`tdur`) of the renderer main
// thread's tasks. `dur` is wall clock and includes the time the thread was taken off the CPU, so a busy host stretches
// it; `tdur` is the time the thread actually ran, so it measures the page instead of the host. The nested timers inside a
// task are not counted again: only the outermost task events are summed.
function mainThreadBusyMs(events: TraceEvent[]): number {
  const main = new Set(
    events
      .filter((event) => event.ph === "M" && event.name === "thread_name" && event.args?.name === "CrRendererMain")
      .map((event) => `${event.pid}:${event.tid}`),
  );
  const tasks = events.filter(
    (event) =>
      event.ph === "X" && main.has(`${event.pid}:${event.tid}`) && event.name === "ThreadControllerImpl::RunTask",
  );
  if (main.size === 0 || tasks.length === 0 || tasks.some((event) => event.tdur === undefined)) {
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
        withTdur: tasks.filter((event) => event.tdur !== undefined).length,
        threads: [...threads],
      })}`,
    );
  }
  return tasks.reduce((total, event) => total + (event.tdur ?? 0), 0) / 1000;
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
