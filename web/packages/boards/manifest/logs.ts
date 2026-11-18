import type { Region } from "../regions.ts";

const english: Record<string, string> = {
  m7dbac1c20f: "Logs",
  m76e547a8fa: "Total ",
  m6fb2840d56: " calls",
  m28384d7afd: "Failed",
  mba00f96381: "Fell back",
  mcff99dcdbb: "Avg TTFT",
  mc69ec2ffb0: "Live",
  md5f5a7a010: "Today",
  m0c184871f6: "Yesterday",
  m2261b06712: "Last 7 days",
  mf729bb3d3f: "Last 30 days",
  m0eeecd26f2: "This month",
  m4eafa9e925: "Custom",
  m0164f80178: "All models",
  mc5c38f461f: "All keys",
  m5c55a67935: "All",
  m61130923cc: "Slower than 5s",
  md32374b79b: "Filter by request ID, model, or key",
  m8b6ff49851: "Time",
  m6320b4a872: "Status",
  mc98e118e0a: "Model",
  m223b24b821: "Route",
  mddab042697: "TTFT",
  me77e3d58b0: "Latency",
  m507c9f9cb6: "Tokens in / out",
  m7fe8603c08: "Cost",
  m053461ce86: "OK",
  m0efcbc2172: "Attempts & route",
  mc6cff8f5d2: "Billing",
  m0e14d148b4: "Speed",
  m301c9461c1: "Identifiers",
  m2087c777c0: "Input",
  mfb04addb4c: "Output",
  m1ded6559f6: "Cache read",
  me009c148d1: "Total",
  m8ef4a4e432: "List price",
  ma154b7d6ee: "Generation speed",
  m0bdffa57e9: "Delivery",
  m62ebdef866: "Streaming",
  m12da486580: "Request ID",
  m2a89973694: "Upstream ID",
  m7a68830642: "Content",
  md9780e8e4e: "Copy cURL",
  m417cfc558e: "Replay in chat",
  ma5089c4cae: "Report call",
  m657d9cbf45: "Clear filters",
  m96c5e762be: "No matching calls.",
  mf29b659412: "This call failed",
  m6eee8c5a90: "View route status",
  mdb667b774d: "Load 60 more",
  m3db3bfd88e: "0s",
  m7b53eb921d: "15s",
};

function viewportOf(board: string) {
  return { width: board === "AxAppPhone" ? 390 : 1440, height: 900 };
}

const unbuiltElements: NonNullable<Region["elements"]> = {
  ".lglive": { unless: "unbuilt" },
  '[data-a="lst"][data-i="3"]': { unless: "unbuilt" },
};

const listTableElements: NonNullable<Region["elements"]> = {
  ".lgl": {
    unless: "hideList",
    set: { "aria-label": "{{tableLabel}}", onclick: "{{onListClick}}" },
  },
  ".lgr": { set: { style: "{{rowStyle}}" } },
  ".lgr > .lgs > i": { unless: "hideStatusDot" },
  ".lgr > .lgt": {
    tag: "link",
    set: {
      href: "#detail",
      style:
        "min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--t2);text-decoration:none;",
    },
  },
  ".lgr > .lgm": {
    tag: "link",
    set: {
      "aria-label": "detail",
      style:
        "min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;border:0;background:none;padding:0;margin:0;color:inherit;text-align:left;cursor:inherit;",
    },
  },
};

const detailPageElements: NonNullable<Region["elements"]> = {
  ...unbuiltElements,
  ".p-logs": { unless: "hidePage", set: { style: "{{pageStyle}}" } },
  ".qph": { unless: "onlyBody" },
  ".usf": { unless: "onlyBody" },
  ".lgsum": { unless: "onlyBody" },
  ".lgw": { set: { style: "{{wrapStyle}}" } },
  ...listTableElements,
};

const detailNavAndIdsElements: NonNullable<Region["elements"]> = {
  ".lgscrim": { unless: "hideScrim", set: { onclick: "{{onClose}}", style: "{{scrimStyle}}" } },
  ".lgd": { unless: "hideDetail", set: { style: "{{detailStyle}}" } },
  ".lgdh .lgm > .lgs": { set: { style: "{{statusBadgeStyle}}" } },
  ".lgdh .lgm > .lgs > i": { set: { style: "{{statusDotStyle}}" } },
  '[data-a="lprev"]': {
    unless: "hideNav",
    set: { onclick: "{{onPrev}}", disabled: "{{prevDisabled}}", "aria-label": "{{prevLabel}}" },
  },
  '[data-a="lnext"]': {
    unless: "hideNav",
    set: { onclick: "{{onNext}}", disabled: "{{nextDisabled}}", "aria-label": "{{nextLabel}}" },
  },
  'button[data-a="lclose"]': {
    unless: "hideNav",
    set: { onclick: "{{onClose}}", "aria-label": "{{closeLabel}}" },
  },
  ".lgid > span": {
    tag: "link",
    set: {
      "data-copy": "0",
      "data-value": "",
      "aria-label": "copy",
      style: "color:var(--t2);font-size:12px;line-height:20px;display:inline-flex;align-items:center;gap:4px;",
    },
  },
};

const detailCardElements: NonNullable<Region["elements"]> = {
  ...detailNavAndIdsElements,
  ".lgtl": { unless: "hideTimeline" },
  ".lgsec:has(.lgct)": { unless: "unbuilt" },
  ".lgact": { unless: "unbuilt" },
};

const attemptRepeats: NonNullable<Region["repeats"]> = {
  steps: ".lgstep",
  speedMetrics: ".lgpf > div",
  idCells: ".lgid > *",
};

const listRepeats: NonNullable<Region["repeats"]> = {
  headers: ".lgh > span",
  rows: ".lgl > :is(.lgday, .lgr)",
};

function baseLogRegions(board: string): Region[] {
  const viewport = viewportOf(board);
  const render: NonNullable<Region["render"]> = {
    root: ".p-logs",
    actions: [
      ["go", "logs"],
      { click: '.p-logs .wv.w2 [data-a="r"][data-i="2"]' },
      { click: '.p-logs .wv.w2 [data-a="lmore"]' },
      { click: '.p-logs .wv.w2 [data-a="lmore"]' },
    ],
    viewport,
  };
  const base: Region = {
    board,
    name: "Logs",
    roots: [".p-logs"],
    omit: [".wv.w0", ".wv.w1"],
    page: "logs",
    render,
    repeats: { summaryItems: ".lgsum > span", ...listRepeats },
    text: { heading: ".p-logs h1", moreLabel: ".lgmore .btn" },
    slots: { pageActions: ".p-logs > .qph > .qpa" },
    elements: {
      ...unbuiltElements,
      ".p-logs": { set: { style: "{{pageStyle}}" } },
      ".qph": { unless: "hideHeader", set: { style: "{{headerStyle}}" } },
      ".qpht": { unless: "hideTitle" },
      ".qpa": { set: { style: "{{actionsStyle}}" } },
      ".lgv": { set: { style: "{{bodyStyle}}" } },
      ".lgx0": { unless: "onlyHeader" },
      ".usf": { unless: "hideFilters" },
      ".lgsum": { unless: "hideSummary", set: { style: "{{summaryStyle}}" } },
      ".lgw": { unless: "hideList", set: { style: "{{wrapStyle}}" } },
      ...listTableElements,
      ".lgmore": { unless: "hideMore" },
      ".lgmore .btn": { set: { onclick: "{{onMore}}", disabled: "{{moreDisabled}}" } },
    },
    english,
  };
  return [base];
}

function logsFiltersRegion(board: string): Region {
  const viewport = viewportOf(board);
  return {
    board,
    name: "LogsFilters",
    roots: [".p-logs"],
    omit: [".wv.w0", ".wv.w1"],
    page: "logs",
    render: {
      root: ".fr",
      actions: [["go", "logs"], { click: '.p-logs .wv.w2 [data-a="chip"][data-i="m"]' }],
      viewport,
    },
    repeats: { options: '.uspop button[data-a="pickm"]' },
    text: { heading: ".p-logs h1", chipLabel: '[data-c="m"] > button', chipClear: '[data-c="m"] .usclr' },
    attributes: { inputPlaceholder: { selector: ".lgqi", attribute: "placeholder" } },
    elements: {
      ...unbuiltElements,
      ".p-logs": { set: { style: "{{pageStyle}}" } },
      ".qph": { unless: "hideHeader" },
      ".lgsum": { unless: "hideSummary" },
      ".lgw": { unless: "hideList" },
      ".usf": { set: { style: "{{barStyle}}" } },
      '[data-s="r"]': {
        unless: "hideRange",
        set: { style: "{{rangeStyle}}", "aria-label": "{{rangeLabel}}", onclick: "{{onRangeClick}}" },
      },
      '[data-c="m"]': { unless: "hideChipM" },
      '[data-c="m"] > button': {
        set: { onclick: "{{onChipToggle}}", style: "{{chipBtnStyle}}", "aria-expanded": "{{chipExpanded}}" },
      },
      '[data-c="m"] .uspop': { unless: "hideChipPop", set: { style: "{{chipPopStyle}}", onclick: "{{onChipPick}}" } },
      '[data-c="m"] .usclr': { unless: "hideChipClear", set: { onclick: "{{onChipClear}}" } },
      '[data-c="k"]': { unless: "hideChipK" },
      '[data-s="lst"]': {
        unless: "hideStatus",
        set: { style: "{{statusStyle}}", "aria-label": "{{statusLabel}}", onclick: "{{onStatusClick}}" },
      },
      ".lgq": { unless: "hideInput", set: { style: "{{inputBoxStyle}}" } },
      ".lgq svg": { unless: "hideInputIcon" },
      ".lgqi": {
        set: {
          type: "{{inputType}}",
          value: "{{inputValue}}",
          "aria-label": "{{inputAriaLabel}}",
          oninput: "{{onInput}}",
        },
      },
    },
    english,
  };
}

function logsEmptyRegion(board: string): Region {
  return {
    board,
    name: "LogsEmpty",
    roots: [".p-logs"],
    omit: [".wv.w0", ".wv.w1"],
    page: "logs",
    render: {
      root: ".p-logs",
      actions: [["go", "logs"], { click: '.p-logs .wv.w2 [data-a="lst"][data-i="2"]' }],
      viewport: viewportOf(board),
    },
    text: { heading: ".p-logs h1", emptyMessage: ".lgempty", clearLabel: ".lgempty .btn" },
    elements: {
      ...unbuiltElements,
      ".p-logs": { set: { style: "{{pageStyle}}" } },
      ".qph": { unless: "onlyEmpty" },
      ".usf": { unless: "onlyEmpty" },
      ".lgsum": { unless: "onlyEmpty" },
      ".lgw": { set: { style: "{{wrapStyle}}" } },
      ".lgl": { set: { style: "{{cardStyle}}" } },
      ".lgempty": { set: { style: "{{boxStyle}}", role: "{{emptyRole}}" } },
      ".lgempty .btn": { unless: "hideClear", set: { onclick: "{{onClear}}" } },
    },
    english,
  };
}

function logsAttemptsRegion(board: string): Region {
  return {
    board,
    name: "LogsAttempts",
    roots: [".p-logs", ".fr > .lgsheet"],
    omit: [".wv.w0", ".wv.w1"],
    page: "logs",
    render: {
      root: ".fr",
      actions: [
        ["go", "logs"],
        { click: '.p-logs .wv.w2 [data-a="r"][data-i="2"]' },
        { click: '.p-logs .wv.w2 [data-a="lmore"]' },
        { click: '.p-logs .wv.w2 [data-a="lmore"]' },
        { click: ".p-logs .wv.w2 .lgr:has(.retry)" },
      ],
      viewport: viewportOf(board),
    },
    repeats: { ...listRepeats, ...attemptRepeats, chargeCells: ".lgkv > span" },
    text: {
      heading: ".p-logs h1",
      detailModel: ".lgdh .lgm",
      detailStatus: ".lgdh .lgm > .lgs",
      detailSub: ".lgdh .lgtt > p",
      attemptsTitle: ".lgsec:has(.lgstep) h3",
      billingTitle: ".lgsec:has(.lgkv) h3",
      speedTitle: ".lgsec:has(.lgpf) h3",
      idsTitle: ".lgsec:has(.lgid) h3",
      tlStart: ".lgtx > span:first-child",
      tlEnd: ".lgtx > span:last-child",
      moreLabel: ".lgmore .btn",
    },
    attributes: {
      modelDotStyle: { selector: ".lgdh .lgm > i", attribute: "style" },
      failBarStyle: { selector: ".lgtl .lgf", attribute: "style" },
      waitBarStyle: { selector: ".lgtl .lgww", attribute: "style" },
      genBarStyle: { selector: ".lgtl .lgg", attribute: "style" },
    },
    elements: {
      ...detailPageElements,
      ".lgmore": { unless: "hideMore" },
      ".lgmore .btn": { set: { onclick: "{{onMore}}", disabled: "{{moreDisabled}}" } },
      ...detailCardElements,
      ".lgsec:has(.lgstep)": { unless: "hideAttempts" },
      ".lgsec:has(.lgkv)": { unless: "hideBilling" },
    },
    english,
  };
}

function logsFailRegion(board: string): Region {
  return {
    board,
    name: "LogsFail",
    roots: [".p-logs", ".fr > .lgsheet"],
    omit: [".wv.w0", ".wv.w1"],
    page: "logs",
    render: {
      root: ".fr",
      actions: [
        ["go", "logs"],
        { click: '.p-logs .wv.w2 [data-a="r"][data-i="2"]' },
        { click: '.p-logs .wv.w2 [data-a="lst"][data-i="2"]' },
        { click: ".p-logs .wv.w2 .lgr ~ .lgr" },
      ],
      viewport: viewportOf(board),
    },
    repeats: { ...listRepeats, speedMetrics: ".lgpf > div", idCells: ".lgid > *" },
    text: {
      heading: ".p-logs h1",
      detailModel: ".lgdh .lgm",
      detailStatus: ".lgdh .lgm > .lgs",
      detailSub: ".lgdh .lgtt > p",
      speedTitle: ".lgsec:has(.lgpf) h3",
      idsTitle: ".lgsec:has(.lgid) h3",
      errLead: ".lgerr",
      errBody: ".lgerr p",
      errBtnLabel: ".lgerr .btn",
    },
    attributes: {
      modelDotStyle: { selector: ".lgdh .lgm > i", attribute: "style" },
    },
    elements: {
      ...detailPageElements,
      ...detailNavAndIdsElements,
      ".lgdh": { unless: "onlyErr" },
      ".lgsec:has(.lgerr)": { set: { style: "{{errSecStyle}}" } },
      ".lgerr": { set: { style: "{{errStyle}}", role: "{{errRole}}" } },
      ".lgerr .btn": { unless: "hideErrBtn", set: { onclick: "{{onErrBtn}}" } },
      ".lgsec:has(.lgstep)": { unless: "hideAttempts" },
      ".lgsec:has(.lgpf)": { unless: "hideSpeed" },
      ".lgsec:has(.lgct)": { unless: "unbuilt" },
      ".lgsec:has(.lgid)": { unless: "hideIds" },
      ".lgact": { unless: "unbuilt" },
    },
    english,
  };
}

function staticWidgetRegions(board: string): Region[] {
  return [
    {
      board,
      name: "LogsBtn",
      roots: [".p-sec .btn.sec.sm"],
      text: { label: ".btn" },
      elements: {
        ".btn": {
          set: { onclick: "{{onClick}}", disabled: "{{disabled}}", style: "{{btnStyle}}" },
        },
      },
      english,
    },
    {
      board,
      name: "LogsSeg",
      roots: [".p-sec .qcard.rows > .qrow:nth-child(2) .qseg"],
      repeats: { items: ".qseg > button" },
      elements: {
        ".qseg": { set: { style: "{{segStyle}}", "aria-label": "{{ariaLabel}}", onclick: "{{onClick}}" } },
        ".qseg > button": { set: { class: "on" } },
      },
      english,
    },
  ];
}

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  ...baseLogRegions(board),
  logsFiltersRegion(board),
  logsEmptyRegion(board),
  logsAttemptsRegion(board),
  logsFailRegion(board),
  ...staticWidgetRegions(board),
]);
