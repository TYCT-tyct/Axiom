import type { Region } from "../regions.ts";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Notify",
    roots: [".p-notify"],
    page: "notify",
    render: {
      root: ".p-notify",
      actions: [["go", "notify"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    repeats: { notifications: ".qnt:not(.hd)" },
  }),
);
