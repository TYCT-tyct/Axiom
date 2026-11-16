import type { CSSProperties } from "react";

function declarations(text: string): CSSStyleDeclaration {
  const sheet = new CSSStyleSheet();
  sheet.insertRule(`x { ${text} }`);
  return (sheet.cssRules[0] as CSSStyleRule).style;
}
const keys = (style: CSSStyleDeclaration) => Array.from({ length: style.length }, (_, i) => style[i] as string);

export function boardStyle(text: string): CSSProperties {
  const style = declarations(text);
  const properties: Record<string, string> = {};
  for (const name of keys(style)) {
    const key = name.startsWith("--")
      ? name
      : name.replace(/^-ms-/, "ms-").replace(/-([a-z])/g, (_, c: string) => c.toUpperCase());
    properties[key] = style.getPropertyValue(name);
  }
  return properties;
}

export function boardCSS(target: CSSStyleDeclaration, text: string, append = false) {
  const source = declarations(text);
  if (!append) for (const key of keys(target)) target.removeProperty(key);
  for (const key of keys(source))
    target.setProperty(key, source.getPropertyValue(key), source.getPropertyPriority(key));
}

function markup(context: Element, html: string) {
  const wrappers: Record<string, string[]> = {
    TABLE: ["table"],
    TBODY: ["table", "tbody"],
    THEAD: ["table", "thead"],
    TFOOT: ["table", "tfoot"],
    TR: ["table", "tbody", "tr"],
    COLGROUP: ["table", "colgroup"],
    SELECT: ["select"],
  };
  const tags = context.namespaceURI === "http://www.w3.org/2000/svg" ? ["svg"] : (wrappers[context.tagName] ?? []);
  const source =
    tags.map((t) => `<${t}>`).join("") +
    html +
    [...tags]
      .reverse()
      .map((t) => `</${t}>`)
      .join("");
  const doc = new DOMParser().parseFromString(`<template>${source}</template>`, "text/html");
  const template = doc.querySelector("template") as HTMLTemplateElement;
  const parent = tags.length ? (template.content.querySelector(tags.join(" > ")) as Element) : template.content;
  const styles: [HTMLElement | SVGElement, string][] = [];
  for (const el of parent.querySelectorAll<HTMLElement | SVGElement>("[style]")) {
    styles.push([el, el.getAttribute("style") ?? ""]);
    el.removeAttribute("style");
  }
  return { nodes: [...parent.childNodes], styles };
}

// Template contents have their own inert document. DOMParser inherits the page's CSP and reports style attributes
// during parsing, before they can be removed. Strip them inside the template, then adopt nodes and use CSSOM.
// Callers still own escaping interpolated text: this adapter preserves the board's behavior, not a sanitizer API.
export function boardHTML(target: Element, html: string, mode: "inner" | "outer" | InsertPosition = "inner") {
  const context = ["outer", "beforebegin", "afterend"].includes(mode) ? (target.parentElement as Element) : target;
  const { nodes, styles } = markup(context, html);
  switch (mode) {
    case "inner":
      target.replaceChildren(...nodes);
      break;
    case "outer":
      target.replaceWith(...nodes);
      break;
    case "beforebegin":
      target.before(...nodes);
      break;
    case "afterend":
      target.after(...nodes);
      break;
    case "afterbegin":
      target.prepend(...nodes);
      break;
    case "beforeend":
      target.append(...nodes);
      break;
  }
  for (const [el, text] of styles) boardCSS(el.style, text);
}
