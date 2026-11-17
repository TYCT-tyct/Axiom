import type { Region } from "../regions.ts";

export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Overview",
    roots: [".p-ov"],
    page: "ov",
    repeats: {
      activity: ".qlvbars > i",
      models: ".ovmr",
      requests: ".qrq:not(.qrqh)",
      days: ".yrsv > g",
      bars: ".qch2 > svg > rect",
    },
    text: {
      noticeTitle: ".qnote > div > b",
      noticeBody: ".qnote > div > span",
      health: ".qlvl > div > b",
      healthDetail: ".qlvl > div > span",
      activityTotal: ".qlvs .num",
      spendWhole: ".qkpi:nth-child(1) > b",
      spendFraction: ".qkpi:nth-child(1) > b > .ct",
      spendChange: ".qkpi:nth-child(1) > em",
      requestTotal: ".qkpi:nth-child(2) > b",
      requestAverage: ".qkpi:nth-child(2) > em",
      latency: ".qkpi:nth-child(3) > b",
      successRate: ".qkpi:nth-child(3) > em",
      budget: ".qkpi:nth-child(4) > b",
      budgetDetail: ".qkpi:nth-child(4) > em",
    },
    attributes: { sparkline: { selector: ".qkpi .qsp polyline", attribute: "points" } },
  }),
);
