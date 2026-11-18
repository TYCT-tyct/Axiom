import type { Region } from "../regions.ts";

// The BYOK page (#747): the account's own provider keys (docs/domains/accounts.md「自带的供应商 key（BYOK）」) in the
// board's page. The board draws four vendors and a workspace-wide "prefer your own key" switch; the API takes one
// provider (OpenAI, #816 O14) and its switch says whether a key may fall back to the site's endpoints for a model it
// covers, which is per key, so the rows list the account's keys and each one's fallback reads in its own line. The
// per-key switch column gives way to the row's name — a button that opens the key's test, switch and removal — because
// the design draws none of them (console-gap L20).
const byokEnglish = {
  m8c4cdb4586:
    "Fill in your own provider key and requests try it first; when it cannot serve a model, routing moves to another line.",
  ma93043c486: "Not added yet",
  m20393ab0a9: ", your own key served 402 calls this month",
  m58aeb636ad: "Bring your own key",
  m54341b67f3: "Only an owner or an administrator can change these settings; you can see them.",
};

function byokRegion(board: string): Region {
  return {
    board,
    name: "Byok",
    roots: [".p-byok"],
    page: "byok",
    omit: [
      // The board draws each workspace's copy of the page and shows the one the shell's level selects (O1, #816).
      ".wv.w0",
      ".wv.w1",
      // A workspace-wide "prefer your own key" is not the API's fallback, which is each key's own (console-gap 1.7).
      ".wv.w2 > .qcard.rows:nth-of-type(2)",
      // The switch per vendor, which is the account's key's own: the row's name opens it instead.
      ".qrow .qrc",
    ],
    slots: { description: ".p-byok .qph p", create: ".p-byok .qph .qpa" },
    repeats: { keys: ".p-byok .qrow" },
    text: { title: ".p-byok .qph h1", notice: ".p-byok .rlro span" },
    elements: {
      // A row's name opens the key: a button's role and keys keep the board's look, as the shell's menu entries do.
      ".p-byok .qrow .qrl b": {
        set: { role: "button", tabindex: "0", "aria-label": "key", onclick: "{{openKey}}", onkeydown: "{{press}}" },
      },
      ".p-byok .qrow": { set: { "data-id": "key" } },
    },
    english: byokEnglish,
  };
}

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(byokRegion);
