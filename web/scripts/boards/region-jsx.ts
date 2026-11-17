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
  return { region, locale, values, texts, replacements, attributes, literal, value, expression };
}

function attribute(name: string, raw: string, state: Emitter, replacement?: string): string {
  const hole = raw.match(/^\{\{\s*([\w.$]+)\s*\}\}$/)?.[1];
  if (name === "ref") return `ref={${state.value(hole as string, "RefCallback<Element>")}}`;
  if (name.startsWith("on")) {
    const event = events[name];
    if (!event || !hole) throw new Error(`unsupported region event ${name}`);
    return `${event}={${state.value(hole, `DOMAttributes<Element>[${JSON.stringify(event)}]`)}}`;
  }
  const expr = replacement ?? state.expression(raw);
  if (name === "style") return `style={boardStyle(${expr})}`;
  if (name === "class") return `className={(${expr}) + " " + styles.scope}`;
  const prop = property(name);
  const booleans = ["disabled", "checked", "selected", "multiple", "readOnly", "required", "autoFocus", "hidden"];
  if (booleans.includes(prop)) return `${prop}={${hole ? state.value(hole, "boolean") : "true"}}`;
  return `${prop === "value" ? "defaultValue" : prop}={${typed(prop, raw, expr)}}`;
}

// React types tabIndex as a number and a state aria-* attribute as "true" | "false"; the board writes strings.
function typed(prop: string, raw: string, expr: string) {
  if (prop === "tabIndex") return `Number(${expr})`;
  if (prop.startsWith("aria-") && raw.includes("{{")) return `(${expr}) as "true" | "false"`;
  return expr;
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
  return children
    ? `<${el.localName} ${attrs.join(" ")}>${children}</${el.localName}>`
    : `<${el.localName} ${attrs.join(" ")} />`;
}
