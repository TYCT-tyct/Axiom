import type { Region } from "../../packages/boards/regions.ts";
import { regionCSS } from "./region-css.ts";
import { emitter, jsx } from "./region-jsx.ts";
import { rows } from "./region-rows.ts";
import { JSDOM } from "./template.ts";

const within = (roots: Element[], selector: string) =>
  roots.flatMap((root) => [...(root.matches(selector) ? [root] : []), ...root.querySelectorAll(selector)]);

function cut(document: Document, region: Region) {
  const roots = region.roots.flatMap((selector) => [...document.querySelectorAll(selector)]);
  if (!roots.length) throw new Error(`${region.board}/${region.name}: roots match nothing`);
  for (const selector of region.omit ?? [])
    for (const root of roots) for (const el of root.querySelectorAll(selector)) el.remove();
  return roots;
}

// A changed tag keeps the board's classes and children, so the board's CSS still selects the element.
export function retag(original: Element, tag: string) {
  const element = original.ownerDocument.createElement(tag);
  for (const { name, value } of original.attributes) element.setAttribute(name, value);
  element.append(...original.childNodes);
  original.replaceWith(element);
  return element;
}

type Change = NonNullable<Region["elements"]>[string];
function change(original: Element, { tag, set = {}, unless }: Change, state: ReturnType<typeof emitter>) {
  if (tag === "link") state.links.add(original);
  const element = tag && tag !== "link" ? retag(original, tag) : original;
  if (unless) state.unless.set(element, unless);
  for (const [name, value] of Object.entries(set)) element.setAttribute(name, value);
}

function edit(roots: Element[], state: ReturnType<typeof emitter>) {
  for (const [selector, declared] of Object.entries(state.region.elements ?? {})) {
    const found = within(roots, selector);
    if (!found.length) throw new Error(`${state.region.board}/${state.region.name}: ${selector} matches nothing`);
    for (const element of found) change(element, declared, state);
  }
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
        const known: Record<string, string[]> = {
          mo: ["mred"],
          cold: ["cold"],
          rootC: rootStates,
          segAc: ["cur", "up"],
        };
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

// The region's slots by prop name: children, and the named ones whose content the page supplies.
const slots = (region: Region): Record<string, string> => ({
  ...(region.children ? { children: region.children } : {}),
  ...region.slots,
});
const slotProps = (names: string[]) => names.map((name) => `${name}?: import("react").ReactNode;`).join(" ");

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
    ...Object.keys(slots(region)),
    state.links.size ? 'link: Link = "a"' : "",
  ]
    .filter(Boolean)
    .join(", ");
  const link = state.links.size
    ? 'link?: import("react").ElementType<import("react").AnchorHTMLAttributes<HTMLAnchorElement>>;'
    : "";
  const props = `t: (key: keyof typeof zh) => string; values?: { ${fields} }; ${hasRows ? `rows: { ${rowFields} };` : ""} ${hasText ? `text: { ${textFields} };` : ""}${link && ` ${link}`}${region.slots ? ` ${slotProps(Object.keys(region.slots))}` : ""}`;
  const inline = [...state.inline]
    .map(([text, name]) => `const ${name} = boardStyle(${JSON.stringify(text)});`)
    .join("\n");
  return `${marker}\n/* jscpd:ignore-start */\n${imports(fields, body + inline, hasRows)}\nimport type zh from "./${region.name}.zh.ts";\nimport styles from "./${region.name}.module.css";\n${inline}\n${rowTypes}\nexport type Props = { ${props} };\nexport default function ${region.name}({ ${args} }: Props${region.children ? ' & { children?: import("react").ReactNode }' : ""}) {\nreturn <>${body}</>;\n}\n/* jscpd:ignore-end */\n`;
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
  const matches = (selector: string) => within(roots, selector);
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
    // The browser fills class holes. Keep their other states for CSS pruning, without emitting harness metadata or
    // changing the captured state in JSX (in particular, a light capture must retain the board's dark-theme rules).
    const authoredClasses = [...document.querySelectorAll("[data-design-class]")].map((element) => {
      const classes = decodeURIComponent(element.getAttribute("data-design-class") as string);
      element.removeAttribute("data-design-class");
      return { element, classes };
    });
    const roots = cut(document, region);
    const state = emitter(region);
    edit(roots, state);
    // A named slot's demo shows the drawing's own content, its text, so the demo still draws the board.
    const slotDemo = new Map<string, string>();
    for (const [name, selector] of Object.entries(slots(region)))
      for (const el of document.querySelectorAll(selector)) {
        if (name !== "children") slotDemo.set(name, state.literal(el.textContent ?? ""));
        el.replaceChildren();
        // Keep the slot's own element in JSX and CSS, replacing only its content.
        el.append(document.createComment(`region ${name}`));
        state.replacements.set(el.firstChild as Node, `{${name}}`);
      }
    expose(roots, state);
    const repeated = rows(roots, state);
    const body = roots.map((root) => jsx(root, state)).join("\n");
    const textDemo = [...state.texts].map(([name, text]) => `${name}: ${state.literal(text)}`).join(",\n");
    const slotsDemo = [...slotDemo].map(([name, value]) => `${name}: ${value},`).join(" ");
    const demo = `({ t, values: {}, ${state.texts.size ? `text: {${textDemo}},` : ""} ${slotsDemo}${repeated.length ? `rows: {${repeated.map((g) => `${g.name}: ${g.demo}`).join(",\n")}}` : ""} })`;
    const elements = new Set(roots.flatMap((root) => [root, ...root.querySelectorAll("*")]));
    for (const { element, classes } of authoredClasses)
      element.setAttribute("class", `${element.getAttribute("class") ?? ""} ${classes}`);
    classStates(document, region, css);
    const files = {
      [`${region.name}.tsx`]: source(region, body, state, repeated, marker),
      [`${region.name}.module.css`]: `${marker}\n/* jscpd:ignore-start */\n${regionCSS(css, elements, region.inserted, region.states)}\n/* jscpd:ignore-end */\n`,
      [`${region.name}.demo.ts`]: `${marker}\n/* jscpd:ignore-start */\nimport type { Props } from "./${region.name}.tsx";\nimport type en from "./${region.name}.en.ts";\nexport const demo = (t: (key: keyof typeof en) => string): Props => ${demo};\n/* jscpd:ignore-end */\n`,
      ...locales(region, state, marker),
    };
    return Object.fromEntries(Object.entries(files).map(([path, text]) => [`${region.board}/regions/${path}`, text]));
  } finally {
    dom.window.close();
  }
}
