import type { Region } from "../regions.ts";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Security",
    roots: [".p-security"],
    page: "security",
    render: {
      root: ".p-security",
      actions: [["go", "security"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    repeats: { sessions: ".qses" },
  }),
);
