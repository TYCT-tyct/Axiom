type CSSBlock = { selector: string; body: string };

// Keep declaration bytes, including strings and data URLs; only selectors and grouping rules are rewritten.
export function cssBlocks(css: string): CSSBlock[] {
  const result: CSSBlock[] = [];
  let start = 0;
  let open = 0;
  let depth = 0;
  const tokens = /\/\*[\s\S]*?\*\/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[{}]/g;
  for (const token of css.matchAll(tokens)) {
    if (token[0] === "{") {
      if (depth++ === 0) open = token.index;
    } else if (token[0] === "}" && --depth === 0) {
      const selector = css
        .slice(start, open)
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .trim();
      result.push({ selector, body: css.slice(open + 1, token.index) });
      start = token.index + 1;
    }
  }
  return result;
}

function selectors(source: string): string[] {
  const result: string[] = [];
  let start = 0;
  let depth = 0;
  const tokens = /"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[()[\],]/g;
  for (const token of source.matchAll(tokens)) {
    if (token[0] === "(" || token[0] === "[") depth++;
    if (token[0] === ")" || token[0] === "]") depth--;
    if (token[0] === "," && depth === 0) {
      result.push(source.slice(start, token.index).trim());
      start = token.index + 1;
    }
  }
  return [...result, source.slice(start).trim()];
}

function potential(selector: string) {
  return selector
    .replace(/::[\w-]+(?:\([^)]*\))?/g, ":is(*)")
    .replace(/:(?:hover|active|focus-visible|focus-within|focus|visited|checked|disabled|enabled)\b/g, ":is(*)")
    .replace(/:not\([^()]*\)/g, ":is(*)");
}

function scoped(selector: string) {
  const at = selector.indexOf("::");
  const subject = at < 0 ? selector : selector.slice(0, at);
  const pseudo = at < 0 ? "" : selector.slice(at);
  // :where contributes no specificity. Original classes also allow ancestors supplied by another region.
  return `:global(${subject}):where(.scope)${pseudo}`;
}

function subject(selector: string) {
  let depth = 0;
  let start = 0;
  for (const token of selector.matchAll(/"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[()[\]\s>+~]/g)) {
    if (token[0] === "(" || token[0] === "[") depth++;
    else if (token[0] === ")" || token[0] === "]") depth--;
    else if (!depth && /^[\s>+~]$/.test(token[0])) start = token.index + 1;
  }
  return selector.slice(start);
}

function matcher(elements: Set<Element>) {
  const index = new Map<string, Element[]>();
  const available = new Set<string>();
  for (const element of elements) {
    const keys = [element.localName, ...[...element.classList].map((name) => `.${name}`)];
    for (const key of keys) {
      const list = index.get(key) ?? [];
      list.push(element);
      index.set(key, list);
    }
    for (let ancestor: Element | null = element; ancestor; ancestor = ancestor.parentElement)
      for (const name of ancestor.classList) available.add(name);
  }
  return (selector: string) => {
    const test = potential(selector);
    if (!/[+~]/.test(test) && requiredClasses(test).some((name) => !available.has(name))) return false;
    const direct = subject(test).match(/^(?:[\w-]+|\*)?(?:\.[\w-]+)*/)?.[0] ?? "";
    const keys = direct.match(/^[\w-]+|\.[\w-]+/g) ?? [];
    const candidates = keys.map((key) => index.get(key) ?? []).sort((a, b) => a.length - b.length)[0] ?? [...elements];
    try {
      return candidates.some((element) => element.matches(test));
    } catch (error) {
      throw new Error(`region CSS selector ${selector} became ${test}`, { cause: error });
    }
  };
}

function requiredClasses(selector: string) {
  let depth = 0;
  const classes: string[] = [];
  for (const token of selector.matchAll(/\[[^\]]*\]|[()]|\.([\w-]+)/g)) {
    if (token[0] === "(") depth++;
    else if (token[0] === ")") depth--;
    else if (!depth && token[1]) classes.push(token[1]);
  }
  return classes;
}

function selectBlocks(blocks: CSSBlock[], matches: (selector: string) => boolean): string {
  return blocks
    .map(({ selector, body }) => {
      if (selector.startsWith("@keyframes")) return `${selector}{${body}}`;
      if (selector.startsWith("@")) {
        const inner = selectBlocks(cssBlocks(body), matches);
        return inner ? `${selector}{${inner}}` : "";
      }
      const kept = selectors(selector).filter(matches);
      return kept.length ? `${kept.map(scoped).join(",")}{${body}}` : "";
    })
    .join("\n");
}

export function regionCSS(css: string, elements: Set<Element>): string {
  const selected = selectBlocks(cssBlocks(css), matcher(elements));
  const withoutFrames = selected.replace(/@keyframes[\s\S]*?\}\s*\}/g, "");
  return cssBlocks(selected)
    .filter(({ selector }) => {
      const name = selector.match(/^@keyframes\s+(\S+)/)?.[1];
      return !name || new RegExp(`\\b${name}\\b`).test(withoutFrames);
    })
    .map(({ selector, body }) => `${selector}{${body}}`)
    .join("\n");
}
