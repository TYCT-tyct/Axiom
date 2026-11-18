import type { Region } from "../regions.ts";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Profile",
    roots: [".p-profile"],
    page: "profile",
    render: {
      root: ".p-profile",
      actions: [["go", "profile"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    text: { avatar: ".qav" },
    attributes: {
      name: { selector: '[data-k="name"]', attribute: "value" },
      email: { selector: '[data-k="mail"]', attribute: "value" },
    },
  }),
);
