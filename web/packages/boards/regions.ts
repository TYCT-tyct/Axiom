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

// Only the page card adds regions. Selectors refer to the pinned design, never to generated JSX.
export const regions: Region[] = ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  {
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
  },
  {
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
  },
]);
