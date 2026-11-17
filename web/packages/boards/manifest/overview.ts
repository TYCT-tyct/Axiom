import type { Region } from "../regions.ts";

// The labels the Overview slice's demo account came with, in English: its manifest file is the only place a translation is
// written, and the generated .en.ts takes them from here (README「画板机械转换」).
const overviewEnglish = {
  mba00f96381: "Retried on another route",
  m053461ce86: "Success",
  mfea405f9b0: "Overview",
  m569c43d6cc: "Copy Base URL",
  mc7b62164df: "New key",
  m02e9734a51: "Spent this month",
  m393e124155: "Requests",
  mfd198d22ce: "Median first token",
  mbd8dbd5724: "Budget",
  ma3cc9c5725: "Daily spend",
  m19e7c0223e: "By model",
  m5073e9f5ed: "See usage",
  m5de8778457: "Recent requests",
  m27975a68f2: "All logs",
  m8b6ff49851: "Time",
  mc98e118e0a: "Model",
  m6320b4a872: "Status",
  m7fe8603c08: "Cost",
  mfa10f1a884: "+23% vs the same days of September",
  m3680acf4d7: "¥0.062 per request on average",
  m7b1ed4bf77: "99.76% succeeded",
  m1ab89346ec: "¥186 of ¥300, ¥248 expected by month end",
};

// The console has only the calls the API can answer: the live state bar, the year heatmap, the month-end forecasts and
// the team workspaces' views stay out (#816, O23). The chart's currency axis goes because the board writes ¥ in its
// CSS; the page's money comes from the account's currency.
function overviewRegion(board: string): Region {
  return {
    board,
    name: "Overview",
    roots: [".p-ov"],
    page: "ov",
    omit: [
      ".qlivec",
      ".qyr",
      ".qleg",
      ".fut",
      ".qtip",
      ".qhits",
      ".qyax",
      ".qgl",
      ".qkpi .qsp",
      ".qkpi:nth-child(1) .cy",
      ".qkpi:nth-child(3) .cy",
      ".qkpi:nth-child(4) .cy",
      ".wv.w0",
      ".wv.w1",
    ],
    repeats: {
      models: ".ovmr",
      requests: ".qrq:not(.qrqh)",
      bars: ".qch2 > svg > rect",
    },
    text: {
      spendWhole: ".qkpi:nth-child(1) > b",
      spendFraction: ".qkpi:nth-child(1) > b > .ct",
      spendNote: ".qkpi:nth-child(1) > em",
      requestTotal: ".qkpi:nth-child(2) > b",
      requestAverage: ".qkpi:nth-child(2) > em",
      balance: ".qkpi:nth-child(3) > b",
      balanceNote: ".qkpi:nth-child(3) > em",
      keyCount: ".qkpi:nth-child(4) > b",
      keyNote: ".qkpi:nth-child(4) > em",
      chartDate: ".qbl span",
    },
    attributes: { chartDateLeft: { selector: ".qbl span", attribute: "style" } },
    english: overviewEnglish,
  };
}

export default ["AxApp", "AxAppPhone"].map(overviewRegion);
