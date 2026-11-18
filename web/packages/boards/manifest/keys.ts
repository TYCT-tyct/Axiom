import type { Region } from "../regions.ts";

// The labels the Keys slice's demo account came with, in English: its manifest file is the only place a translation is
// written, and the generated .en.ts takes them from here (README「画板机械转换」).
const keysEnglish = {
  mc7b62164df: "New key",
  // The demo rows' own text: the page writes its rows from the API.
  m79e8d00e7c: "All models",
  ma04a019c92: "IP restricted",
  mde6785d99e: "Just now",
  m362e205987: "Any IP",
  m536df87c9b: "43 minutes ago",
  m5085291b0c: "2 models",
  mfce7232ef8: "12 minutes ago",
  m0c184871f6: "Yesterday",
  mcd4b7219e4:
    "A key works only in the workspace it was made in, and its spending is recorded under the person who made it.",
  md44e9b3d3b: "Name",
  mc77c96d4f5: "Used / limit this month",
  m0188e32b20: "Last 14 days",
  m688f892378: "Limits",
  m39a9046f9c: "Last used",
};

// The board draws the key list once per space (#747 C1): the default workspace's, a team's, and the personal space's,
// which the shell selects with wa2. The console has no team spaces (#816 O1), so only the personal list stays. Its
// header is the page's; the actions beside it are the page's own, because the board's list has no filters and the
// filters an account already had (L8) open from there. The used column's header is a text the page supplies: the board
// calls it the month's spending, and what the API can say is what the key used of its limit's own period.
function keysRegion(board: string): Region {
  return {
    board,
    name: "Keys",
    roots: [".p-keys"],
    page: "keys",
    omit: [".wv.w0", ".wv.w1"],
    slots: { actions: ".p-keys .qpa" },
    repeats: { keys: ".wv.w2 .qkeyt tbody > tr" },
    text: { usedHeader: ".wv.w2 .qkeyt thead th:nth-child(2)" },
    // The rows open the key they stand for, as the board's own rows do, and also from the keyboard. They stay rows of
    // the table: a role of their own would take the cells' headers away from a reader, and the page's own tests and the
    // journey read them as rows. The icons say nothing to a reader, and their aria-hidden is the page's.
    elements: {
      ".wv.w2 .qkeyt tbody > tr": { set: { tabindex: "{{rowTab}}", onkeydown: "{{press}}" } },
      ".wv.w2 .qkeyt tbody svg": { set: { "aria-hidden": "{{decorative}}" } },
    },
    english: keysEnglish,
  };
}

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(keysRegion);
