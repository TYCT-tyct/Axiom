// @vitest-environment jsdom
import i18next from "i18next";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { startI18n } from "../../../packages/ui/src/i18n";
import en from "../locales/en";
import zh from "../locales/zh";
import { type Context, reply } from "./terminal";

const model = (id: string, routes?: number) => ({
  id,
  name: id.split("/")[1] ?? id,
  author: id.split("/")[0] ?? id,
  context_length: 1,
  max_output: 1,
  streams: true,
  abilities: [],
  endpoints:
    routes === undefined
      ? undefined
      : Array.from({ length: routes }, (_, i) => ({
          name: `g${i}`,
          context_length: 1,
          max_output: 1,
          streams: true,
          abilities: [],
          verified: true,
        })),
});

function context(over: Partial<Context> = {}): Context {
  return {
    t: i18next.t,
    models: async () => ({ data: [model("acme/sonnet", 2), model("deepseek/v3", 1), model("deepseek/r1")] }),
    trip: async () => 84,
    host: "axiom.test",
    open: false,
    acts: { feed: vi.fn(), dive: vi.fn(), surface: vi.fn(), whale: vi.fn(), milestone: vi.fn() },
    ...over,
  };
}

beforeAll(() => {
  localStorage.setItem("axiom.language", "en");
  startI18n("site", { en, zh });
});

describe("the sea-floor terminal", () => {
  it.each([
    { line: "help", first: "ls           list the models and their routes", count: 8 },
    { line: "  WHOAMI ", first: "A curious developer.", count: 2 },
    { line: "sudo rm -rf /", first: "Sharks don't take sudo.", count: 1 },
    { line: "fly", first: "fly: no such command, try help", count: 1 },
    { line: "constructor", first: "constructor: no such command, try help", count: 1 },
  ])("answers $line", async ({ line, first, count }) => {
    const got = await reply(line, context());
    expect(got).not.toBeTypeOf("string");
    if (typeof got === "string") return;
    expect(got.print[0]).toBe(first);
    expect(got.print).toHaveLength(count);
  });

  it("aligns help's columns, a Chinese word taking two", async () => {
    await i18next.changeLanguage("zh");
    const got = await reply("help", context());
    await i18next.changeLanguage("en");
    if (typeof got === "string") throw new Error(got);
    expect(got.print.slice(0, 2)).toEqual(["ls           列出模型和线路", "ping [模型]  测一次到本站的往返"]);
  });

  it.each([
    { line: "clear", want: "clear" },
    { line: "exit", want: "close" },
    { line: "quit", want: "close" },
  ])("$line answers $want", async ({ line, want }) => {
    expect(await reply(line, context())).toBe(want);
  });

  it.each([
    { line: "feed", act: "feed", said: "Here come the fish." },
    { line: "dive", act: "dive", said: "Diving −1200m" },
    { line: "surface", act: "surface", said: "Surfacing −0m" },
    { line: "whale", act: "whale", said: "Here comes the whale." },
  ] as const)("$line does it to the page", async ({ line, act, said }) => {
    const c = context();
    expect(await reply(line, c)).toEqual({ print: [said] });
    expect(c.acts[act]).toHaveBeenCalledOnce();
  });

  it("lists the site's own models with their routes, and says when there are more or none", async () => {
    expect(await reply("ls", context())).toEqual({
      print: ["acme/sonnet  2 routes", "deepseek/v3  1 route", "deepseek/r1"],
      step: 40,
    });
    const many = { data: Array.from({ length: 11 }, (_, i) => model(`m/${i}`)) };
    const got = await reply("ls", context({ models: async () => many }));
    if (typeof got === "string") throw new Error(got);
    expect(got.print).toHaveLength(9);
    expect(got.print.at(-1)).toBe("3 more, the full list is at /models");
    expect(await reply("ls", context({ models: async () => ({ data: [] }) }))).toEqual({
      print: ["No models are listed yet"],
    });
    expect(await reply("ls", context({ models: async () => null }))).toEqual({
      print: ["ls: the model list is unavailable, try again later"],
    });
  });

  it("pings the site, after the models named, and rings once answered", async () => {
    const c = context();
    expect(await reply("ping deepseek", c)).toEqual({
      print: ["deepseek/v3  listed", "deepseek/r1  listed", "axiom.test  round trip 84ms  ok"],
      step: 160,
    });
    expect(c.acts.milestone).toHaveBeenCalledOnce();
    expect(await reply("ping nova", c)).toEqual({ print: ["ping: no model called nova, try ls"] });
    const silent = context({ trip: async () => null });
    expect(await reply("ping", silent)).toEqual({ print: ["ping: no answer from axiom.test this time"], step: 160 });
    expect(silent.acts.milestone).not.toHaveBeenCalled();
  });

  it("says signing up takes a minute only where anyone may sign up", async () => {
    expect(await reply("whoami", context({ open: true }))).toEqual({
      print: ["A curious developer.", "Signing up takes a minute. One key for every model it may use."],
    });
  });
});
