import type { Region } from "../regions.ts";

const english = {
  m5c5c50127e: "Low balance",
  mf905cecae0: "When balance covers 7 days left",
  mbe3ca95001: "Low balance email",
  m6c2f20b394: "Low balance in-app",
  mefb0a3f0fd: "Budget alert",
  mf938889e52: "When budget reaches the alert threshold",
  m05f6c8f16a: "Budget alert email",
  ma55585c4b8: "Budget alert in-app",
  m21b9c2c0f5: "Key limit reached",
  m6ebe4c218a: "When one of your keys reaches its monthly limit",
  m96c7d670ac: "Key limit reached email",
  m53dfc7462c: "Key limit reached in-app",
  m4331923669: "Route failure",
  m8db33f0cf4: "When an active route keeps failing",
  mbd63e1d068: "Route failure email",
  m89f980112a: "Route failure in-app",
  m22504c61e2: "Invitation accepted",
  m48894e6fbd: "When a member or guest joins",
  m77f2f043e3: "Invitation accepted email",
  mdab03fef49: "Invitation accepted in-app",
  m1c9b5c1f34: "Weekly usage summary",
  md972ccb2cd: "Every Monday morning",
  md5ddf0326f: "Weekly usage summary email",
  mbb747c8c9d: "Weekly usage summary in-app",
  m4b6ded8a7f: "Notifications",
  m512b281f73: "When and how you are notified.",
  m3d708e8e8b: "Email",
  m36e623b19e: "In-app",
};

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
    elements: {
      ".p-notify": { set: { style: "{{pageStyle}}" } },
      ".qph": { unless: "hideHead" },
      ".qnt": { set: { style: "{{rowStyle}}" } },
      ".qnt.hd > span:nth-child(3)": { unless: "singleChannel" },
      ".qnt:not(.hd) > button:nth-of-type(1)": { set: { onclick: "{{onToggle}}" } },
      ".qnt:not(.hd) > button:nth-of-type(2)": { unless: "singleChannel" },
    },
    english,
  }),
);
