import type { Schemas } from "@axiom/api";
import type { TFunction } from "i18next";

// The sea-floor terminal's commands (the design's eggs.js Terminal): a line typed in, an answer out. The design's ls
// and ping print a made-up list of models and random latencies; here ls lists the site's own published models (the
// answer the models screen shows, public-models.ts) and ping, as the public API publishes no latency, times this
// browser's round trip to the site and says so.

type Models = Schemas["PublicModelList"];

// what the commands do to the page
type Acts = {
  feed: () => void;
  dive: () => void;
  surface: () => void;
  whale: () => void;
  milestone: () => void;
};

export type Context = {
  t: TFunction;
  // the site's published models, or null when they cannot be had
  models: () => Promise<Models | null>;
  // one round trip to the site in milliseconds, or null without an answer
  trip: () => Promise<number | null>;
  host: string;
  // whether anyone may sign up (whoami says it takes a minute only then)
  open: boolean;
  acts: Acts;
};

// What the terminal does: print lines (each step ms after the one before), clear its screen, or close.
export type Reply = { print: string[]; step?: number } | "clear" | "close";

// ls shows this many models, so its answer and the line typed fit the terminal's ten lines
const listed = 8;

// A Chinese character takes two columns of a monospace line.
const columns = (text: string) => [...text].reduce((n, c) => n + ((c.codePointAt(0) ?? 0) > 0x2e7f ? 2 : 1), 0);
const pad = (text: string, width: number) => text + " ".repeat(Math.max(1, width - columns(text)));

const say = (...print: string[]): Reply => ({ print });

const help = ({ t }: Context) => {
  const rows: [string, string][] = [
    ["ls", t("eggs.help.ls")],
    [t("eggs.help.ping"), t("eggs.help.pingDoes")],
    ["feed", t("eggs.help.feed")],
    ["dive", t("eggs.help.dive")],
    ["surface", t("eggs.help.surface")],
    ["whoami", t("eggs.help.whoami")],
    ["whale", t("eggs.help.whale")],
    ["clear", t("eggs.help.clear")],
  ];
  return say(...rows.map(([name, does]) => pad(name, 13) + does));
};

async function ls({ t, models }: Context): Promise<Reply> {
  const list = await models();
  if (!list) return say(t("eggs.ls.failed"));
  if (!list.data.length) return say(t("eggs.ls.none"));
  const shown = list.data.slice(0, listed);
  const width = Math.min(34, Math.max(...shown.map((m) => m.id.length)) + 2);
  const print = shown.map((m) =>
    m.endpoints ? pad(m.id, width) + t("script.models.routes", { count: m.endpoints.length }) : m.id,
  );
  if (list.data.length > listed) print.push(t("eggs.ls.more", { count: list.data.length - listed }));
  return { print, step: 40 };
}

// ping [model]: the models whose ID or name starts with model, then the round trip to the site
async function ping(name: string | undefined, c: Context): Promise<Reply> {
  const { t, host } = c;
  const print: string[] = [];
  if (name) {
    const list = await c.models();
    if (!list) return say(t("eggs.ls.failed"));
    const found = list.data.filter((m) => m.id.toLowerCase().startsWith(name) || m.name.toLowerCase().startsWith(name));
    if (!found.length) return say(t("eggs.ping.missing", { name }));
    print.push(...found.slice(0, 5).map((m) => t("eggs.ping.listed", { model: m.id })));
  }
  const ms = await c.trip();
  if (ms === null) return { print: [...print, t("eggs.ping.failed", { host })], step: 160 };
  c.acts.milestone();
  return { print: [...print, t("eggs.ping.trip", { host, ms })], step: 160 };
}

const act = (key: keyof Acts, line: "feed" | "dive" | "surface" | "whale") => (c: Context) => {
  c.acts[key]();
  return say(c.t(`eggs.${line}`));
};

const commands: Record<string, (c: Context, args: string[]) => Reply | Promise<Reply>> = {
  help,
  ls,
  ping: (c, [name]) => ping(name, c),
  feed: act("feed", "feed"),
  dive: act("dive", "dive"),
  surface: act("surface", "surface"),
  whale: act("whale", "whale"),
  whoami: ({ t, open }) =>
    say(t("eggs.whoami"), open ? t("eggs.minute", { tagline: t("script.bed.tagline") }) : t("script.bed.tagline")),
  clear: () => "clear",
  exit: () => "close",
  quit: () => "close",
  sudo: ({ t }) => say(t("eggs.sudo")),
};

// reply answers one line typed into the terminal; the words are taken in lower case, as the design's.
export async function reply(line: string, c: Context): Promise<Reply> {
  const [name = "", ...args] = line.trim().toLowerCase().split(/\s+/);
  const command = Object.hasOwn(commands, name) ? commands[name] : undefined;
  return command ? command(c, args) : say(c.t("eggs.unknown", { command: name }));
}
