import type { Region } from "../regions.ts";

const usageEnglish: Record<string, string> = {
  m2087c777c0: "Input",
  mfb04addb4c: "Output",
  m1ded6559f6: "Cache read",
  m6078a681a3: "Cache write",
  m42723e8b49: "Usage",
  me40a628ffe: "Export CSV",
  md5f5a7a010: "Today",
  m2261b06712: "7d",
  mf729bb3d3f: "30d",
  m0eeecd26f2: "This month",
  m4eafa9e925: "Custom",
  m0164f80178: "All models",
  m5c55a67935: "All",
  mc5c38f461f: "All keys",
  mb08febb9ae: "vs Sep 1 – 24",
  m311fd2d94c: "Spend",
  m393e124155: "Requests",
  mfadeb36471: "Per 1M tokens",
  m698ae0907a: "Cache hit",
  m60c1886f22: "Daily spend",
  m929b43dca6: "Projected ¥248 this month, budget ¥300",
  m19e7c0223e: "By model",
  m2d0cbd5c89: "By key",
  mc98e118e0a: "Model",
  mcc87b27b64: "Share",
  m3c94b4c759: "Input tokens",
  ma026f37e61: "Output tokens",
  m7f2028d8f8: "Avg / req",
  me009c148d1: "Total",
  m719cac6836: "Token composition",
  m0010f9bb02: "Cache hit 26.5%, saved ",
  mc5f3b8b1a2: "Top movers",
  m634bef649b: "−3.7 pp",
  m0c184871f6: "Yesterday",
  m0cf8b14f46: "Last week",
  m692c48b73e: "Last month",
  m0909522d90: "Last 90 days",
  m51a75f4634: "Mo",
  m084b42f6e9: "Tu",
  ma4c3313deb: "We",
  m754a9d5828: "Th",
  mc9b87f516a: "Fr",
  mde07b53838: "Sa",
  m85217f7aff: "Su",
  mf9e7272234: "Previous month",
  m3e0b634699: "Next month",
  m2cd0f3be87: "Cancel",
  mfac2a67ad8: "Apply",
  m31374f35b9: "Aug 2026",
  mf7a2df289d: "Sep 2026",
  m16f911a943: "Sep 5 – 9, 5 days",
};

const usageFields: Omit<Region, "board" | "name" | "roots" | "page" | "omit" | "render"> = {
  slots: { tip: ".ustip" },
  repeats: {
    ranges: '.qseg[data-s="r"] > button',
    modelOpts: '.uschip[data-c="m"] .uspop button[data-a="pickm"]',
    keyOpts: '.uschip[data-c="k"] .uspop button[data-a="pickk"]',
    segs: ".usc .qch .usct:nth-child(2) > .qseg",
    gridLines: ".usgl > line",
    bars: ".usch svg rect",
    yLabels: ".usyl > span",
    xLabels: ".usxl > span",
    tableHeaders: ".ust thead tr > th",
    models: ".ust tbody > tr",
    tokenBars: ".ustk .usbar > i",
    tokens: ".ustk .usrw",
    movers: ".usd .usrw",
  },
  text: {
    spendCurrency: ".usm > div:nth-child(1) .cy",
    ppmCurrency: ".usm > div:nth-child(4) .cy",
    totals: "[data-tw]",
    changes: ".usm em",
    modelChipLabel: '.uschip[data-c="m"] > button',
    keyChipLabel: '.uschip[data-c="k"] > button',
    cmp: ".usf .uscmp",
    chartTitle: ".usc .qch h2",
    tfootTotals: ".ust tfoot td:nth-child(n+2):not(.ussh)",
    cacheHitNote: ".ustk p",
  },
  attributes: { chartViewBox: { selector: ".usch svg", attribute: "viewBox" } },
  elements: {
    ".usf": { set: { ref: "{{filterBarRef}}" } },
    ".qph .qpa button": { set: { onclick: "{{onExport}}", disabled: "{{exportDisabled}}" } },
    ".usx": { unless: "hideBody", set: { onclick: "{{onBodyClick}}" } },
    ".usm, .usc, .usb": { unless: "hideCharts" },
    ".usc .usfc": { unless: "omitForecast" },
    ".ustk p b": { unless: "omitSavings" },
    '.qseg[data-s="r"]': { set: { style: "--n: 5; --i: 3;{{rangeSegStyle}}" } },
    '.uschip[data-c="m"]': { set: { class: "uschip {{modelChipCls}}" } },
    '.uschip[data-c="m"] > button': {
      set: { class: "{{modelChipBtnCls}}", "aria-expanded": "{{modelChipExpanded}}" },
    },
    '.uschip[data-c="m"] .uspop button[data-a="pickm"]:first-child': {
      set: { "data-st": "1", class: "{{_stOpt}}" },
    },
    '.uschip[data-c="k"]': { set: { class: "uschip {{keyChipCls}}" } },
    '.uschip[data-c="k"] > button': {
      set: { class: "{{keyChipBtnCls}}", "aria-expanded": "{{keyChipExpanded}}" },
    },
    ".usch": {
      set: {
        class: "usch {{chartCls}}",
        onmousemove: "{{onChartMove}}",
        onmouseleave: "{{onChartLeave}}",
        onkeydown: "{{onChartKeyDown}}",
      },
    },
    ".usch svg": { set: { role: "img", tabindex: "0", "aria-label": "{{chartAria}}" } },
    ".usch svg rect.ushit:nth-of-type(1)": { set: { "data-st": "1", class: "ushit {{_stHit}}" } },
    ".usch svg rect.ushit + rect.usbar": { set: { "data-st": "1", class: "usbar {{_stBar}}" } },
    ".ustip": { set: { class: "ustip {{tipCls}}", style: "{{tipStyle}}" } },
    ".ust tbody > tr:first-child": { set: { "data-st": "1", class: "{{_stRow}}" } },
  },
  classes: {
    modelChipCls: ["open"],
    modelChipBtnCls: ["on"],
    keyChipCls: ["open"],
    keyChipBtnCls: ["on"],
    chartCls: ["hl"],
    tipCls: ["on"],
    _stOpt: ["on"],
    _stHit: ["cur"],
    _stBar: ["on"],
    _stRow: ["on", "dim"],
  },
  english: usageEnglish,
};

function makeUsageRegion(board: string): Region {
  const viewport = { width: board === "AxAppPhone" ? 390 : 1440, height: 900 };
  return {
    board,
    name: "Usage",
    roots: [".p-usage"],
    page: "usage",
    omit: [".wv.w0", ".wv.w1"],
    render: { root: ".p-usage", actions: [["go", "usage"]], viewport },
    ...usageFields,
  };
}

function makeCalendarRegion(board: string): Region {
  const viewport = { width: board === "AxAppPhone" ? 390 : 1440, height: 900 };
  const cnavPrev =
    board === "AxAppPhone"
      ? '.p-usage .wv.w2 .usmo:last-child [data-a="cnav"][data-i="-1"]'
      : '.p-usage .wv.w2 .usmo:first-child [data-a="cnav"][data-i="-1"]';
  return {
    board,
    name: "UsageCalendar",
    roots: [".p-usage"],
    page: "usage",
    omit: [".wv.w0", ".wv.w1"],
    render: {
      root: ".p-usage",
      actions: [
        ["go", "usage"],
        { click: '.p-usage .wv.w2 [data-a="r"][data-i="4"]' },
        { click: cnavPrev },
        { click: '.p-usage .wv.w2 .usdd[data-a="cd"][data-i="66"]' },
        { click: '.p-usage .wv.w2 .usdd[data-a="cd"][data-i="70"]' },
      ],
      viewport,
    },
    repeats: {
      quickBtns: ".usql > button",
      weekdaysLeft: ".usmo:nth-child(1) .usgr .usdw",
      weekdaysRight: ".usmo:nth-child(2) .usgr .usdw",
      leftCells: ".usmo:nth-child(1) .usgr > :not(.usdw)",
      rightCells: ".usmo:nth-child(2) .usgr > :not(.usdw)",
    },
    text: { months: ".usmo .usmh b", calStatus: ".uscf > span" },
    elements: {
      ".qph": { unless: "calendarOnly" },
      ".usv > :not(.usf)": { unless: "calendarOnly" },
      ".usf > :not(.uscal)": { unless: "calendarOnly" },
      ".p-usage": { set: { style: "{{calOverlayStyle}}" } },
      ".uscal": { set: { onclick: "{{onCalClick}}", onmousemove: "{{onCalMove}}", onkeydown: "{{onCalKeyDown}}" } },
      ".usmo:nth-child(1) .usnv.p": { set: { disabled: "{{prevMonthDisabled}}" } },
      ".usmo:nth-child(2) .usnv.p": { set: { disabled: "{{prevMonthDisabled}}" } },
      ".usmo:nth-child(2) .usnv.n": { set: { disabled: "{{nextMonthDisabled}}" } },
      '.uscf button[data-a="cok"]': { set: { disabled: "{{confirmDisabled}}" } },
      ".usmo:first-child .usdd:last-child": { set: { "data-st": "1", class: "usdd {{_stTd}}" } },
    },
    classes: { _stTd: ["td", "x"] },
    english: usageEnglish,
  };
}

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  makeUsageRegion(board),
  makeCalendarRegion(board),
]);
