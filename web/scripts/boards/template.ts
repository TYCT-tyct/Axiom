import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
export const { JSDOM } = require("jsdom") as {
  JSDOM: new (html: string) => { window: { document: Document; close(): void } };
};
export const events: Record<string, string> = {
  onclick: "onClick",
  oninput: "onInput",
  onchange: "onChange",
  onkeydown: "onKeyDown",
  onmouseenter: "onMouseEnter",
  onmouseleave: "onMouseLeave",
  onmousemove: "onMouseMove",
};
const names: Record<string, string> = {
  class: "className",
  for: "htmlFor",
  tabindex: "tabIndex",
  readonly: "readOnly",
  maxlength: "maxLength",
  autocomplete: "autoComplete",
  autofocus: "autoFocus",
  inputmode: "inputMode",
  spellcheck: "spellCheck",
  colspan: "colSpan",
  rowspan: "rowSpan",
  viewbox: "viewBox",
  preserveaspectratio: "preserveAspectRatio",
};
const boolean = new Set(["disabled", "checked", "selected", "multiple", "readOnly", "required", "autoFocus", "hidden"]);

function expression(text: string): string {
  const parts: string[] = [];
  let start = 0;
  for (const match of text.matchAll(/\{\{\s*([\w.$]+)\s*\}\}/g)) {
    if (match.index > start) parts.push(JSON.stringify(text.slice(start, match.index)));
    parts.push(`String(v.${match[1]} ?? "")`);
    start = match.index + match[0].length;
  }
  if (start < text.length) parts.push(JSON.stringify(text.slice(start)));
  return parts.join(" + ") || '""';
}

export function property(name: string) {
  if (name.startsWith("aria-") || name.startsWith("data-")) return name;
  return names[name] ?? name.replace(/[-:]([a-z])/g, (_, c: string) => c.toUpperCase());
}

let refIndex = 0;
function attribute(name: string, value: string) {
  const hole = value.match(/^\{\{\s*([\w.$]+)\s*\}\}$/)?.[1];
  if (name === "ref") return `ref={bind(${JSON.stringify(`${hole}:${refIndex++}`)}, v.${hole})}`;
  if (name.startsWith("on")) {
    if (!events[name] || !hole) throw new Error(`unsupported event ${name}=${value}`);
    return `${events[name]}={v.${hole}}`;
  }
  if (name === "style") return `style={boardStyle(${expression(value)})}`;
  const prop = property(name);
  // The boards own these inputs imperatively; React must not reset an edited value on unrelated state changes.
  const input = prop === "value" ? "defaultValue" : prop === "checked" ? "defaultChecked" : prop;
  return `${input}={${boolean.has(prop) ? (hole ? `v.${hole}` : "true") : expression(value)}}`;
}

function nodeJSX(node: Node): string {
  if (node.nodeType === 3) return `{${expression(node.textContent ?? "")}}`;
  if (node.nodeType !== 1) return "";
  const el = node as Element;
  const tag = el.localName;
  if (tag === "script" || tag === "sc-for") throw new Error(`unsupported template element: ${tag}`);
  const attributes = [...el.attributes].map((a) => attribute(a.name, a.value)).join(" ");
  const children = [...el.childNodes].map(nodeJSX).join("");
  return children ? `<${tag} ${attributes}>${children}</${tag}>` : `<${tag} ${attributes} />`;
}

export function template(source: string): string {
  refIndex = 0;
  const dom = new JSDOM(`<body>${source}</body>`);
  const result = [...dom.window.document.body.childNodes].map(nodeJSX).join("\n");
  dom.window.close();
  return result;
}
