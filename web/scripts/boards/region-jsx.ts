import { createHash } from "node:crypto";
import type { Region } from "../../packages/boards/regions.ts";
import { events, property } from "./template.ts";

export type Emitter = ReturnType<typeof emitter>;

export function emitter(region: Region) {
  const locale: Record<string, string> = {};
  const values = new Map<string, string>();
  const texts = new Map<string, string>();
  const replacements = new Map<Node, string>();
  const attributes = new Map<Attr, string>();
  const links = new Set<Element>();
  const unless = new Map<Element, string>();
  // A style with no hole is parsed once, when the module loads, not on every render: a constant per distinct text.
  const inline = new Map<string, string>();
  const constant = (text: string) => {
    if (!inline.has(text)) inline.set(text, `inline${inline.size}`);
    return inline.get(text) as string;
  };
  const literal = (text: string) => {
    if (!/\p{Script=Han}/u.test(text)) return JSON.stringify(text);
    const key = `m${createHash("sha256").update(text).digest("hex").slice(0, 10)}`;
    locale[key] = text;
    return `t(${JSON.stringify(key)})`;
  };
  const value = (key: string, type = "string | number") => {
    if (key.includes(".")) throw new Error(`nested region hole needs an explicit item template: ${key}`);
    const prior = values.get(key);
    values.set(key, prior && prior !== type ? `${prior} & ${type}` : type);
    return `v.${key}`;
  };
  const expression = (text: string, type?: string): string => {
    const pieces: string[] = [];
    let start = 0;
    for (const match of text.matchAll(/\{\{\s*([\w.$]+)\s*\}\}/g)) {
      if (match.index > start) pieces.push(literal(text.slice(start, match.index)));
      pieces.push(`String(${value(match[1] as string, type)} ?? "")`);
      start = match.index + match[0].length;
    }
    if (start < text.length) pieces.push(literal(text.slice(start)));
    return pieces.join(" + ") || '""';
  };
  return {
    region,
    locale,
    values,
    texts,
    replacements,
    attributes,
    links,
    unless,
    inline,
    constant,
    literal,
    value,
    expression,
  };
}

const booleans = ["disabled", "checked", "selected", "multiple", "readOnly", "required", "autoFocus", "hidden"];
// ARIA states a page sets from its own state, which React types as booleans.
const ariaStates = ["aria-expanded", "aria-pressed", "aria-checked", "aria-selected", "aria-disabled", "aria-hidden"];
// The props whose value React wants other than as the attribute's text.
const typed: Record<string, (expr: string) => string> = {
  className: (expr) => `className={(${expr}) + " " + styles.scope}`,
  tabIndex: (expr) => `tabIndex={Number(${expr})}`,
  value: (expr) => `defaultValue={${expr}}`,
};

// A style with no hole is a module constant; one with holes is parsed on each render.
const style = (raw: string, state: Emitter, replacement?: string) =>
  replacement === undefined && !raw.includes("{{")
    ? state.constant(raw)
    : `boardStyle(${replacement ?? state.expression(raw)})`;

function attribute(name: string, raw: string, state: Emitter, replacement?: string): string {
  const hole = raw.match(/^\{\{\s*([\w.$]+)\s*\}\}$/)?.[1];
  if (name === "ref") return `ref={${state.value(hole as string, "RefCallback<Element>")}}`;
  if (name.startsWith("on")) {
    const event = events[name];
    if (!event || !hole) throw new Error(`unsupported region event ${name}`);
    return `${event}={${state.value(hole, `DOMAttributes<Element>[${JSON.stringify(event)}]`)}}`;
  }
  const prop = property(name);
  if (booleans.includes(prop) || (hole && ariaStates.includes(prop)))
    return `${prop}={${hole ? state.value(hole, "boolean") : "true"}}`;
  if (name === "style") return `style={${style(raw, state, replacement)}}`;
  const expr = replacement ?? state.expression(raw);
  return typed[prop]?.(expr) ?? `${prop}={${expr}}`;
}

export function jsx(node: Node, state: Emitter, fields?: Map<Node | Attr, string>): string {
  const replacement = state.replacements.get(node);
  if (replacement !== undefined) return replacement;
  const field = fields?.get(node);
  if (node.nodeType === 3) return `{${field ?? state.expression(node.textContent ?? "")}}`;
  if (node.nodeType !== 1) return "";
  const el = node as Element;
  if (["script", "sc-for"].includes(el.localName)) throw new Error(`unsupported region element ${el.localName}`);
  const attrs = [...el.attributes].map((a) =>
    attribute(a.name, a.value, state, fields?.get(a) ?? state.attributes.get(a)),
  );
  if (!el.hasAttribute("class")) attrs.push("className={styles.scope}");
  const children = [...el.childNodes].map((child) => jsx(child, state, fields)).join("");
  const tag = state.links.has(el) ? "Link" : el.localName;
  const out = children ? `<${tag} ${attrs.join(" ")}>${children}</${tag}>` : `<${tag} ${attrs.join(" ")} />`;
  const hole = state.unless.get(el);
  return hole ? `{!${state.value(hole, "boolean")} && (${out})}` : out;
}
