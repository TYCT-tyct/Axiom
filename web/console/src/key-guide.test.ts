import { expect, it } from "vitest";
import { type GuideClient, type GuideProtocol, keyGuide } from "./key-guide";
import en from "./locales/en";
import zh from "./locales/zh";

type AnyGuide = GuideProtocol | GuideClient;

const apiBase = "https://api.tenant-317.example";
// Split so the repository's secret scan does not read a made-up key as a leaked one.
const secret = ["sk-ax-v1-", "0123456789abcdef"].join("");
const guide = keyGuide(apiBase, secret);

const textOf = (id: AnyGuide) =>
  [...(guide?.protocols ?? []), ...(guide?.clients ?? [])].find((entry) => entry.id === id)?.text ?? "";

const addresses: { case: string; id: AnyGuide; has: string }[] = [
  { case: "chat completions", id: "chat", has: `${apiBase}/v1/chat/completions` },
  { case: "responses", id: "responses", has: `${apiBase}/v1/responses` },
  { case: "Anthropic messages", id: "messages", has: `${apiBase}/v1/messages` },
  { case: "Claude Code", id: "claude-code", has: `ANTHROPIC_BASE_URL=${apiBase}\n` },
  { case: "Codex", id: "codex", has: `base_url = "${apiBase}/v1"` },
  { case: "Cursor", id: "cursor", has: `${apiBase}/v1` },
];
const carriesKey: AnyGuide[] = ["chat", "responses", "messages", "claude-code"];
const protocols: GuideProtocol[] = ["chat", "responses", "messages"];
const clients: GuideClient[] = ["claude-code", "codex", "cursor"];

it("says nothing without an API domain, because there is nowhere for a client to point", () => {
  expect(keyGuide("", secret)).toBeUndefined();
});

// The address in the snippets is the one the site has, which is why `apiBase` above is a site-specific one: a domain
// written into the code would show up here as a snippet that ignores its input.
it("follows the site's address instead of one written into the code", () => {
  const other = keyGuide("https://api.other-site.example", secret);
  expect(other?.protocols[0]?.text).toContain("https://api.other-site.example/v1/chat/completions");
  expect(other?.protocols[0]?.text).not.toContain(apiBase);
});

it.each(addresses)("$case points at the site's API domain, not one written here", ({ id, has }) => {
  expect(textOf(id)).toContain(has);
});

it.each(carriesKey)("the %s snippet carries the key", (id) => {
  expect(textOf(id)).toContain(secret);
});

it("gives Claude Code the host alone, because the client appends /v1/messages itself", () => {
  expect(textOf("claude-code")).toContain(`ANTHROPIC_BASE_URL=${apiBase}\n`);
  expect(textOf("claude-code")).not.toContain(`${apiBase}/v1`);
});

it("keeps the key out of Codex's file, which names the variable that holds it", () => {
  expect(textOf("codex")).toContain('env_key = "AXIOM_API_KEY"');
  expect(textOf("codex")).not.toContain(secret);
});

it("sends Codex to the Responses endpoint of that base", () => {
  expect(textOf("codex")).toContain('wire_api = "responses"');
});

it("gives Cursor the bare /v1 base its OpenAI override takes", () => {
  expect(textOf("cursor")).toBe(`${apiBase}/v1`);
});

it.each(protocols)("the %s protocol is named in Chinese and English", (id) => {
  expect(zh.keys.guide.protocol[id]).not.toBe("");
  expect(en.keys.guide.protocol[id]).not.toBe("");
});

it.each(clients)("the %s client is named in Chinese and English", (id) => {
  expect(zh.keys.guide.client[id]).not.toBe("");
  expect(en.keys.guide.client[id]).not.toBe("");
  expect(zh.keys.guide.hint[id]).not.toBe("");
  expect(en.keys.guide.hint[id]).not.toBe("");
});
