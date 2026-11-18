import type { Region } from "../regions.ts";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Usage",
    roots: [".p-usage"],
    page: "usage",
    render: {
      root: ".p-usage",
      actions: [["go", "usage"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    repeats: { models: ".ust tbody > tr", tokens: ".usrw", bars: ".usch svg rect" },
    text: { totals: "[data-tw]", changes: ".usm em" },
  }),
);
