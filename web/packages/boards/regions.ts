export type Region = {
  board: string;
  name: string;
  roots: string[];
  page?: string;
  omit?: string[];
  children?: string;
  repeats?: Record<string, string>;
  text?: Record<string, string>;
  attributes?: Record<string, { selector: string; attribute: string }>;
  classes?: Record<string, string[]>;
  english?: Record<string, string>;
};

// The labels the Overview slice's demo account came with, in English: regions.ts is the only place a translation is
// written, and the generated .en.ts takes them from here (README「画板机械转换」).
const overviewEnglish = {
  mba00f96381: "Retried on another route",
  m053461ce86: "Success",
  mfea405f9b0: "Overview",
  m569c43d6cc: "Copy Base URL",
  mc7b62164df: "New key",
  m3b09c4395d: "Adjust limit",
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
  ma5ed23e046: "production has used 86% of its quota",
  m56c750f710:
    "¥431.60 / ¥500. At this rate it runs out around October 28; after that this key's requests are refused.",
  mf7beee2958: "+34% vs the same days of September",
  m41d168728e: "¥0.075 per request on average",
  mc7bac034f2: "99.82% succeeded",
  m70521d196a: "¥842 of ¥1,500, ¥1,138 expected by month end",
};

function shellRegion(board: string): Region {
  return {
    board,
    name: "Shell",
    roots: [".board"],
    omit: [".qdrs", ".qdr", ".kov", ".tst"],
    children: ".qpages",
    text: {
      workspaceSpent: ".qwsr .pmt > span",
      workspaceName: ".qwsr .pmt > b",
      ...(board === "AxApp"
        ? {
            accountName: ".qspm .qspi:first-child .pmt > span",
            personalBalance: ".gbal .sxP",
            teamBalance: ".gbal .sxT",
            spaceBalance: ".qspb",
            balanceWhole: ".bpv",
            balanceFraction: ".bpv .ct",
            balanceRunway: ".bp .bpt",
            balanceDate: ".bp .bpt > b",
            autoRecharge: ".bp .bpr .pmt > span",
            teamMembers: ".qspm .qspi:nth-child(2) .pmt > span",
          }
        : { spaceBalance: ".qswc .num" }),
    },
  };
}

// The console has only the calls the API can answer: the live state bar, the year heatmap, the month-end forecasts and
// the other demo accounts' views stay out (#816, O23). The chart's currency axis goes because the board writes ¥ in its
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
      ".wv.w1",
      ".wv.w2",
    ],
    repeats: {
      models: ".ovmr",
      requests: ".qrq:not(.qrqh)",
      bars: ".qch2 > svg > rect",
    },
    text: {
      noticeTitle: ".qnote > div > b",
      noticeBody: ".qnote > div > span",
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

// Only the page card adds regions. Selectors refer to the pinned design, never to generated JSX.
export const regions: Region[] = ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  shellRegion(board),
  overviewRegion(board),
]);
