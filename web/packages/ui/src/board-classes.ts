// Board class names from @axiom/boards/AxApp/regions.css that @axiom/ui primitives render directly instead of
// duplicating their rules in component CSS Modules. Every value here must appear in AxApp/regions.css (checked by test).
export const board = {
  // Scope root for overlays and standalone pages outside ConsoleShell / AdminShell.
  board: "board",
  ax: "ax",
  ov: "ov",
  // Buttons (.btn pri/sec/gho/dan/sm).
  btn: "btn",
  pri: "pri",
  sec: "sec",
  gho: "gho",
  dan: "dan",
  sm: "sm",
  // Cards (.qcard/.flush/.rows/.danger, header .qch).
  qcard: "qcard",
  flush: "flush",
  rows: "rows",
  danger: "danger",
  qch: "qch",
  // Tables (.qtw/.qtbl).
  qtw: "qtw",
  qtbl: "qtbl",
  r: "r",
  mut: "mut",
  mono: "mono",
  // Tags (.qtag ok/warn/fail/beta).
  qtag: "qtag",
  ok: "ok",
  warn: "warn",
  fail: "fail",
  beta: "beta",
  // Avatars (.qav sm/lg).
  qav: "qav",
  lg: "lg",
  // Setting rows (.qrow .qrl .qrc).
  qrow: "qrow",
  qrl: "qrl",
  qrc: "qrc",
  // Segmented controls (.qseg) and switches (.sw2.qsw.on).
  qseg: "qseg",
  sw2: "sw2",
  qsw: "qsw",
  on: "on",
  // Text inputs (.qfl .qin).
  qfl: "qfl",
  qin: "qin",
  // State blocks (.stb .stbi .stba .stbid).
  stb: "stb",
  err: "err",
  stbi: "stbi",
  stba: "stba",
  stbid: "stbid",
  // Dialogs and right-hand side panels (.nkdlg .side .nkh .nkb .nkfoot .nkx .ppsaved .ppdel).
  nkdlg: "nkdlg",
  side: "side",
  nkh: "nkh",
  nkb: "nkb",
  nkfoot: "nkfoot",
  nkx: "nkx",
  ppsaved: "ppsaved",
  ppdel: "ppdel",
  // Popovers and menus (.pw .pm .pm.r .pm.open .pmi .ck).
  pw: "pw",
  pm: "pm",
  open: "open",
  pmi: "pmi",
  ck: "ck",
  // Toasts (.fxt ok/bad on/out).
  fxt: "fxt",
  bad: "bad",
  out: "out",
} as const;
