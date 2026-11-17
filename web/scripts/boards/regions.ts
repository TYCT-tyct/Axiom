import type { Region } from "../../packages/boards/regions.ts";
import { regionCSS } from "./region-css.ts";
import { emitter, jsx } from "./region-jsx.ts";
import { rows } from "./region-rows.ts";
import { JSDOM } from "./template.ts";

function cut(document: Document, region: Region) {
  const roots = region.roots.flatMap((selector) => [...document.querySelectorAll(selector)]);
  if (!roots.length) throw new Error(`${region.board}/${region.name}: roots match nothing`);
  for (const selector of region.omit ?? [])
    for (const root of roots) for (const el of root.querySelectorAll(selector)) el.remove();
  return roots;
}

const states = ["on", "off", "open", "active", "sel", "bad", "warn", "ok", "hot", "in", "out"];
function classStates(document: Document, region: Region, css: string) {
  const classes = [...new Set([...css.matchAll(/\.([a-zA-Z_][\w-]*)/g)].map((match) => match[1] as string))];
  const rootStates = classes
    .filter((name) =>
      /^(lv|sp|wo|wa|tm|ap|pg-|lf|wsk|nks|cz|ck|ct|den|thD|rl|pst|kp|yrc|pico)|^(rw|tx\d|dny)$/.test(name),
    )
    .filter((name) => !region.page || !name.startsWith("pg-") || name === `pg-${region.page}`);
  for (const element of document.querySelectorAll("[class]")) {
    element.setAttribute(
      "class",
      (element.getAttribute("class") ?? "").replace(/\{\{\s*(\w+)\s*\}\}/g, (_, hole: string) => {
        const known: Record<string, string[]> = { mo: ["mred"], cold: ["cold"], rootC: rootStates };
        return (region.classes?.[hole] ?? known[hole] ?? states).join(" ");
      }),
    );
  }
}

function imports(fields: string, body: string, hasRows: boolean) {
  const names = [
    hasRows ? "Fragment" : "",
    fields.includes("DOMAttributes") ? "type DOMAttributes" : "",
    fields.includes("RefCallback") ? "type RefCallback" : "",
  ]
    .filter(Boolean)
    .join(", ");
  return [
    names ? `import { ${names} } from "react";` : "",
    body.includes("boardStyle(") ? 'import { boardStyle } from "../../dom.ts";' : "",
  ].join("\n");
}

function source(
  region: Region,
  body: string,
  state: ReturnType<typeof emitter>,
  repeated: ReturnType<typeof rows>,
  marker: string,
) {
  const fields = [...state.values].map(([name, type]) => `${name}?: ${type};`).join("\n");
  const rowTypes = repeated.map((group) => group.type).join("\n");
  const rowFields = repeated.map((group) => `${group.name}: readonly ${group.typeName}[];`).join("\n");
  const textFields = [...state.texts].map(([name]) => `${name}: string;`).join("\n");
  const hasText = state.texts.size > 0;
  const hasRows = repeated.length > 0;
  const args = [
    body.includes("t(") ? "t" : "",
    fields ? "values: v = {}" : "",
    hasRows ? "rows" : "",
    hasText ? "text" : "",
    region.children ? "children" : "",
  ]
    .filter(Boolean)
    .join(", ");
  const props = `t: (key: keyof typeof zh) => string; values?: { ${fields} }; ${hasRows ? `rows: { ${rowFields} };` : ""} ${hasText ? `text: { ${textFields} };` : ""}`;
  return `${marker}\n/* jscpd:ignore-start */\n${imports(fields, body, hasRows)}\nimport type zh from "./${region.name}.zh.ts";\nimport styles from "./${region.name}.module.css";\n${rowTypes}\nexport type Props = { ${props} };\nexport default function ${region.name}({ ${args} }: Props${region.children ? ' & { children?: import("react").ReactNode }' : ""}) {\nreturn <>${body}</>;\n}\n/* jscpd:ignore-end */\n`;
}

function locales(region: Region, state: ReturnType<typeof emitter>, marker: string) {
  const locale = JSON.stringify(state.locale, null, 2);
  const english = JSON.stringify(
    Object.fromEntries(Object.keys(state.locale).map((key) => [key, region.english?.[key] ?? ""])),
    null,
    2,
  );
  return {
    [`${region.name}.zh.ts`]: `${marker}\n/* jscpd:ignore-start */\nexport default ${locale} as const;\n/* jscpd:ignore-end */\n`,
    [`${region.name}.en.ts`]: `${marker}\n/* jscpd:ignore-start */\nimport type zh from "./${region.name}.zh.ts";\nexport default ${english} satisfies Record<keyof typeof zh, string>;\n/* jscpd:ignore-end */\n`,
  };
}

function expose(roots: Element[], state: ReturnType<typeof emitter>) {
  const matches = (selector: string) => roots.flatMap((root) => [...root.querySelectorAll(selector)]);
  const bind = (name: string, nodes: (Node | Attr)[]) => {
    if (!nodes.length) throw new Error(`${state.region.name}: ${name} matches nothing`);
    for (const [index, node] of nodes.entries()) {
      const key = nodes.length === 1 ? name : `${name}${index}`;
      state.texts.set(key, node.nodeValue ?? "");
      if (node.nodeType === 2) state.attributes.set(node as Attr, `text.${key}`);
      else state.replacements.set(node, `{text.${key}}`);
    }
  };
  for (const [name, selector] of Object.entries(state.region.text ?? {}))
    bind(
      name,
      matches(selector).flatMap((el) => [...el.childNodes].filter((n) => n.nodeType === 3 && n.textContent?.trim())),
    );
  for (const [name, { selector, attribute }] of Object.entries(state.region.attributes ?? {}))
    bind(
      name,
      matches(selector).map((el) => el.getAttributeNode(attribute) as Attr),
    );
}

export function convertRegion(markup: string, css: string, region: Region, marker: string): Record<string, string> {
  const dom = new JSDOM(`<body>${markup}</body>`);
  const document = dom.window.document;
  try {
    const roots = cut(document, region);
    const state = emitter(region);
    if (region.children)
      for (const el of document.querySelectorAll(region.children)) {
        el.replaceChildren();
        // Keep the slot's own element in JSX and CSS, replacing only its content.
        el.append(document.createComment("region children"));
        state.replacements.set(el.firstChild as Node, "{children}");
      }
    expose(roots, state);
    const repeated = rows(roots, state);
    const body = roots.map((root) => jsx(root, state)).join("\n");
    const textDemo = [...state.texts].map(([name, text]) => `${name}: ${state.literal(text)}`).join(",\n");
    const demo = `({ t, values: {}, ${state.texts.size ? `text: {${textDemo}},` : ""} ${repeated.length ? `rows: {${repeated.map((g) => `${g.name}: ${g.demo}`).join(",\n")}}` : ""} })`;
    const elements = new Set(roots.flatMap((root) => [root, ...root.querySelectorAll("*")]));
    classStates(document, region, css);
    const files = {
      [`${region.name}.tsx`]: source(region, body, state, repeated, marker),
      [`${region.name}.module.css`]: `${marker}\n/* jscpd:ignore-start */\n${regionCSS(css, elements)}\n/* jscpd:ignore-end */\n`,
      [`${region.name}.demo.ts`]: `${marker}\n/* jscpd:ignore-start */\nimport type { Props } from "./${region.name}.tsx";\nimport type en from "./${region.name}.en.ts";\nexport const demo = (t: (key: keyof typeof en) => string): Props => ${demo};\n/* jscpd:ignore-end */\n`,
      ...locales(region, state, marker),
    };
    return Object.fromEntries(Object.entries(files).map(([path, text]) => [`${region.board}/regions/${path}`, text]));
  } finally {
    dom.window.close();
  }
}
