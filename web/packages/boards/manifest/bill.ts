import type { Region } from "../regions.ts";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Bill",
    roots: [".p-bill"],
    page: "bill",
    render: {
      root: ".p-bill",
      actions: [["go", "bill"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    repeats: { entries: ".qtbl tbody > tr" },
  }),
);
