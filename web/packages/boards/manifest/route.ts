import type { Region } from "../regions.ts";

// The Routing page (#747): the console's routing presets (docs/domains/routing.md「路由预设」) in the board's route page.
// The board draws a workspace's supplier lines with their latency, availability and price, a switch per line, and a
// failover switch and timeouts (#816 O13): no endpoint names a line, a failed attempt is the platform's, and what a key
// routes by is a preset. So the table lists presets, its last column gives way to the row's own name — a button that
// opens the console's editor, which the design has none of (console-gap L2) — and the two cards above it are left out.
const routeEnglish = {
  m2ab93e3755: "When a request names no line, this workspace picks by these rules.",
  mf940739d4d: "Line A",
  mb770360c59: "Official, direct",
  m296de0e31f: "Fine",
  mf55252e7da: "0.82 s",
  mbe5bdd33a5: "Official price",
  ma9ef9e0623: "Line B",
  m376cbd8cfc: "Third party",
  m9d029bd063: "0.95 s",
  m4113faff49: "23% cheaper",
  m03667795a2: "Line C",
  m9c1e885e8f: "0.71 s",
  mbbf4f33047: "31% cheaper",
  mc390b4e5b1: "Line D",
  mefe09e7ac6: "Unsteady",
  m97f979871d: "1.42 s",
  m53b0eaf669: "35% cheaper",
  mb9670c85a4: "Routing",
  m54341b67f3: "Only an owner or an administrator can change these settings; you can see them.",
  m223b24b821: "Line",
  m2a60026a5f: "A line switched off is never picked, not even by name.",
  m6320b4a872: "Status",
  mfd198d22ce: "Median first token",
  mcd1c07b148: "Up 7 days",
  m0f4371a7a9: "Price",
  mf4f0ead111: "Enabled",
};

function routeRegion(board: string): Region {
  return {
    board,
    name: "Route",
    roots: [".p-route"],
    page: "route",
    omit: [
      // The board draws each workspace's copy of the page and shows the one the shell's level selects; the console keeps
      // the account's own, the second variant (O1, #816).
      ".wv.w0",
      ".wv.w1",
      // A workspace's sort preference, its failover switch and its timeouts have no account-level setting: a preset's
      // rules carry its own sort, and a failed attempt is handled by the platform (#816 O13).
      ".qstrc",
      ".qcard.rows",
      // The switch per line. A preset's default for new keys is one choice per account rather than one per row, so it
      // lives on the account's default in the row's own dialog, and the column goes.
      ".qtbl thead th:nth-child(6)",
      ".qtbl tbody td:nth-child(6)",
    ],
    // The page's own paragraph and its action have the console's words and controls.
    slots: { description: ".p-route .qph p", create: ".p-route .qph .qpa" },
    repeats: { presets: ".qtbl tbody tr" },
    text: {
      title: ".qph h1",
      notice: ".rlro span",
      listTitle: ".qcard.flush .qch h2",
      listHint: ".qcard.flush .qch p",
      colName: ".qtbl thead th:nth-child(1)",
      colLayer: ".qtbl thead th:nth-child(2)",
      colRules: ".qtbl thead th:nth-child(3)",
      colKeys: ".qtbl thead th:nth-child(4)",
      colVersion: ".qtbl thead th:nth-child(5)",
    },
    elements: {
      // A row's name opens the preset: a button's role and keys keep the board's look, as the shell's menu entries do.
      ".qtbl tbody .qkn b": {
        set: {
          role: "button",
          tabindex: "0",
          "aria-label": "preset",
          onclick: "{{openPreset}}",
          onkeydown: "{{press}}",
        },
      },
      // The row's id, which the handler reads, and whether it is what a new key takes, which the design marks in words
      // the page supplies: the design scene finds the default's row whichever language the page is in.
      ".qtbl tbody tr": { set: { "data-id": "preset", "data-default": "false" } },
    },
    english: routeEnglish,
  };
}

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(routeRegion);
