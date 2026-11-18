import type { Region } from "../regions.ts";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].flatMap((board): Region[] => {
  const render: NonNullable<Region["render"]> = {
    root: ".p-logs",
    actions: [["go", "logs"]],
    viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
  };
  const base: Region = {
    board,
    name: "Logs",
    roots: [".p-logs"],
    page: "logs",
    render,
    repeats: { requests: ".lgr" },
    text: { summary: ".lgsum b" },
    elements: { ".lgv": { set: { onclick: "{{click}}", oninput: "{{input}}" } } },
  };
  const selected = (name: string, click: string): Region => ({
    ...base,
    name,
    // On phones the board moves the scrim and detail to the frame, outside the page.
    roots: [".p-logs", ".fr > .lgsheet"],
    render: { ...render, root: ".fr", actions: [["go", "logs"], { click }] },
  });
  return [
    base,
    selected("LogsFilters", '.p-logs .wv.w2 [data-a="chip"][data-i="m"]'),
    selected("LogsDetail", ".p-logs .wv.w2 .lgr"),
    selected("LogsAttempts", ".p-logs .wv.w2 .lgr:has(.retry)"),
  ];
});
