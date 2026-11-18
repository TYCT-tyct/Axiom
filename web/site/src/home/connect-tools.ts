import { hash } from "@axiom/fx";
import { keyGuide } from "@axiom/ui";
import type zh from "../locales/zh";

// The configurations the connect screen shows for each tool (Site.dc.html M1's TOOLS): lines of runs, the address and
// the key being runs of their own. The address is the site's (the board's is a placeholder); the key is a link to sign
// up, where a reader gets one.
type Word = keyof typeof zh.script.connect;
export type Run = ["t", string] | ["w", Word] | ["u", "root" | "v1"] | ["k"];
// a line is a comment, a plain line, a line you add (decoded in and marked +), or the one that runs it
type Line = { kind: "c" | "n" | "a" | "r"; runs: Run[] };
export type Tool = { name: Run; file: Run; lines: Line[] };

const c = (word: Word): Line => ({ kind: "c", runs: [["w", word]] });
const a = (...runs: Run[]): Line => ({ kind: "a", runs });
const n = (...runs: Run[]): Line => ({ kind: "n", runs });
const blank = n();
const t = (text: string): Run => ["t", text];
const key: Run = ["k"];
const w = (word: Word): Run => ["w", word];

// The configurations of Claude Code and Codex, and the address Cursor takes, are the console's key guide's (packages/ui
// key-guide.ts), so what the home page shows and what the console's new-key dialog hands over are one text. Built with
// stand-ins for the site's address and the key, which the lines show as runs and a copy fills in.
const standIn = { root: "\u0001root\u0001", key: "\u0001key\u0001" };
const guide = keyGuide(standIn.root, standIn.key);
const guided = (id: "claude-code" | "codex" | "cursor") =>
  guide?.clients.find((client) => client.id === id)?.text ?? "";
// a line of the guide's text as runs: the address (with /v1 or without) and the key stand where the guide puts them
const guideLine = (text: string): Line => {
  const runs: Run[] = text
    .split(new RegExp(`(${standIn.root}/v1|${standIn.root}|${standIn.key})`))
    .filter(Boolean)
    .map((part): Run => {
      if (part === `${standIn.root}/v1`) return ["u", "v1"];
      if (part === standIn.root) return ["u", "root"];
      return part === standIn.key ? key : t(part);
    });
  return a(...runs);
};
const guideLines = (id: "claude-code" | "codex") => guided(id).split("\n").filter(Boolean).map(guideLine);

export const tools: Tool[] = [
  {
    name: t("Claude Code"),
    file: w("terminal"),
    lines: [c("alsoZshrc"), ...guideLines("claude-code"), blank, { kind: "r", runs: [t("claude")] }],
  },
  // the file's lines only: the key is set in the terminal, and the guide's file does not hold it
  {
    name: t("Codex"),
    file: t("~/.codex/config.toml"),
    lines: [...guideLines("codex"), c("setKey"), a(t("export AXIOM_API_KEY="), key)],
  },
  {
    name: t("Cursor"),
    file: w("cursorSettings"),
    lines: [a(t("Override Base URL   "), ["u", "v1"]), a(t("OpenAI API Key      "), key), blank, c("pickModel")],
  },
  {
    name: t("OpenAI SDK"),
    file: t("main.py"),
    lines: [
      n(t("from openai import OpenAI")),
      blank,
      n(t("client = OpenAI(")),
      a(t('    base_url="'), ["u", "v1"], t('",')),
      a(t('    api_key="'), key, t('",')),
      n(t(")")),
      c("modelList"),
      { kind: "r", runs: [t('client.responses.create(model="<model>", input="'), w("hello"), t('")')] },
    ],
  },
  {
    name: w("other"),
    file: t(".env"),
    lines: [
      c("compatOpenai"),
      a(t("OPENAI_BASE_URL="), ["u", "v1"]),
      a(t("OPENAI_API_KEY="), key),
      blank,
      c("compatAnthropic"),
      a(t("ANTHROPIC_BASE_URL="), ["u", "root"]),
      a(t("ANTHROPIC_AUTH_TOKEN="), key),
    ],
  },
];
// the index of the line whose run is the address, and the one with the key
export const addressLine = tools.map((tool) => tool.lines.findIndex((line) => line.runs.some((run) => run[0] === "u")));
export const keyLine = tools.map((tool) => tool.lines.findIndex((line) => line.runs.some((run) => run[0] === "k")));

// known: whether the site has an API address to put in them; without one there is nothing to copy
export type Texts = { word: (word: Word) => string; root: string; v1: string; known: boolean };
// a run's text: what it says in the language and for the site
export const textOf = (run: Run, { word, root, v1 }: Texts) => {
  if (run[0] === "t") return run[1];
  if (run[0] === "w") return word(run[1]);
  if (run[0] === "u") return run[1] === "root" ? root : v1;
  return "";
};
const GLYPHS = ":;t%SX8#@/<>=+*";

// What the tool's configuration copies: for the tools the key guide knows, its text; for the others, the lines with their
// comments left out. The key is a placeholder, and the model one to fill in from the key's model list.
export function copyText(index: number, texts: Texts) {
  const tool = tools[index] as Tool;
  const secret = `<${texts.word("yourKey")}>`;
  const id = (["claude-code", "codex", "cursor"] as const)[index];
  const fill = (text: string) => text.replaceAll(standIn.root, texts.root).replaceAll(standIn.key, secret);
  if (id) return fill(guided(id));
  return tool.lines
    .filter((line) => line.kind !== "c")
    .map((line) => line.runs.map((run) => (run[0] === "k" ? secret : textOf(run, texts))).join(""))
    .join("\n")
    .trim();
}

// How far the decode has got: the characters written so far (null once all are), the tick the tumbling glyphs follow,
// and whether the card is the phone's.
export type Decode = { written: number | null; tick: number; narrow: boolean };

// One run of an added line mid-decode: what is written, the glyphs still tumbling, and what is yet to come. offset is the
// number of added characters before the run.
function decoding(text: string, offset: number, { written, tick }: Decode) {
  const into = Math.max(0, Math.min(text.length, (written ?? 0) - offset));
  const glyphs = Math.max(0, Math.min(text.length - into, (written ?? 0) + 3 - offset - into));
  const tumbling = [...text.slice(into, into + glyphs)]
    .map((ch, m) => (ch === " " ? " " : GLYPHS[Math.floor(hash(offset + into + m, tick) * GLYPHS.length)]))
    .join("");
  return { written: text.slice(0, into), tumbling, rest: text.slice(into + glyphs), complete: into >= text.length };
}
// on the phone the lines hold their spaces and may break after the address
const phone = (text: string) => text.replace(/ /g, "\u00a0");

// The holes of one of a line's three runs (m1_l<i>A, G and C: what is written, the glyphs tumbling, the rest), and
// whether the run is all written.
function runHoles(
  p: string,
  j: number,
  run: Run | undefined,
  texts: Texts,
  decode: Decode,
  adding: boolean,
  offset: number,
) {
  const text = run ? textOf(run, texts) : "";
  const live = adding && decode.written !== null;
  const parts = live ? decoding(text, offset, decode) : { written: text, tumbling: "", rest: "", complete: true };
  // on the phone the lines hold their spaces and may break after the address
  const wrap = decode.narrow && adding;
  const [first, glyphs, last] = [parts.written, parts.tumbling, parts.rest].map((part) => (wrap ? phone(part) : part));
  return {
    complete: parts.complete,
    length: text.length,
    holes: {
      [`${p}k${j}`]: run?.[0] === "u" ? "adr" : "",
      [`${p}A${j}`]: wrap && run?.[0] === "u" ? `\u200b${first}` : (first as string),
      [`${p}G${j}`]: glyphs as string,
      [`${p}C${j}`]: last as string,
    },
  };
}

// The holes of one line of the card (m1_l<i>…): its class, number, mark and the three runs of its text. seen counts the
// characters added before the line.
function lineHoles(i: number, line: Line, texts: Texts, decode: Decode, seen: number, pulse: boolean) {
  const p = `m1_l${i}`;
  const adding = line.kind === "a";
  const holes: Record<string, string> = {
    [`${p}c`]: `${{ c: "cm", n: "n", a: "ad", r: "rn" }[line.kind]}${pulse ? " pl" : ""}`,
    [`${p}n`]: String(i + 1),
    [`${p}m`]: adding ? "+" : " ",
  };
  const runs = line.runs.filter((run) => run[0] !== "k");
  let complete = true;
  let offset = seen;
  for (let j = 0; j < 3; j++) {
    const done = runHoles(p, j, runs[j], texts, decode, adding, offset);
    Object.assign(holes, done.holes);
    complete &&= done.complete;
    offset += done.length;
  }
  holes[`${p}K`] = line.runs.some((run) => run[0] === "k") ? (complete ? "on" : "wait") : "";
  return holes;
}

// Every hole of the card's lines for a tool: the rows the tool has, and the rest of the nine drawn empty.
export function cardHoles(index: number, texts: Texts, decode: Decode, pulse: boolean, rows: number) {
  const tool = tools[index] as Tool;
  const holes: Record<string, string> = {};
  // how many characters of what is added come before each line, for the decode that runs through them all
  let seen = 0;
  for (let i = 0; i < rows; i++) {
    const line = tool.lines[i];
    if (!line) {
      Object.assign(holes, emptyLine(i));
      continue;
    }
    Object.assign(holes, lineHoles(i, line, texts, decode, seen, pulse && i === addressLine[index]));
    if (line.kind === "a") seen += line.runs.reduce((sum, run) => sum + textOf(run, texts).length, 0);
  }
  return holes;
}
// the character count of everything the tool adds, which the decode runs through
export const addedLength = (tool: Tool, texts: Texts) =>
  tool.lines
    .filter((line) => line.kind === "a")
    .reduce((sum, line) => sum + line.runs.reduce((s, run) => s + textOf(run, texts).length, 0), 0);

function emptyLine(i: number) {
  const p = `m1_l${i}`;
  const holes: Record<string, string> = { [`${p}c`]: "off", [`${p}n`]: "", [`${p}m`]: "", [`${p}K`]: "" };
  for (let j = 0; j < 3; j++)
    Object.assign(holes, { [`${p}k${j}`]: "", [`${p}A${j}`]: "", [`${p}G${j}`]: "", [`${p}C${j}`]: "" });
  return holes;
}
