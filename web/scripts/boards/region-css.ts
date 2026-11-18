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

// The run-time states a region declares (Region.states): classes an engine or script toggles (on, wh-lit) and attributes it
// sets (data-tab). Their rules are kept, as the markup does not carry them as cut.
const stateless = (selector: string, states: readonly string[]) =>
  states.reduce((text, name) => {
    const escaped = name.replace(/[-\\^$*+?.()|[\]{}]/g, "\\$&");
    return /^(data|aria)-/.test(name)
      ? text.replace(new RegExp(`\\[${escaped}(?:[~|^$*]?=(?:"[^"]*"|'[^']*'|[^\\]]*))?\\]`, "g"), "")
      : text.replace(new RegExp(`\\.${escaped}(?![\\w-])`, "g"), "");
  }, selector);

function potential(selector: string, states: readonly string[]) {
  return (
    stateless(selector, states)
      .replace(/::[\w-]+(?:\([^)]*\))?/g, ":is(*)")
      .replace(/:(?:hover|active|focus-visible|focus-within|focus|visited|checked|disabled|enabled)\b/g, ":is(*)")
      // The board is cut as its markup first stands: the page's language and the states engines and components set at
      // run time (the data-* the Site engines set, aria-expanded="true") are not in it yet, so their rules are kept
      // regardless. Only those: widening further copies rules into regions that never match them.
      .replace(/:lang\([^()]*\)/g, "")
      // the one class an engine adds itself: prefs.js marks the language row the arrow keys are on
      .replace(/(\.lp li)\.act(?![\w-])/g, "$1")
      .replace(/\[data-(?:open|enter|eff|on|t)(?:[~|^$*]?=(?:"[^"]*"|'[^']*'|[^\]]*))?\]/g, ":is(*)")
      .replace(/\[(aria-[\w-]+)[~|^$*]?=(?:"[^"]*"|'[^']*'|[^\]]*)\]/g, "[$1]")
      .replace(/:not\([^()]*\)/g, ":is(*)")
  );
}

// scoped ties a rule to the region: its subject carries the region's scope class, or, for markup an engine inserts at
// run time (an SVG fetched and parsed into place), whose nodes carry no scope class of their own, sits inside an
// element that does. :where contributes no specificity; original classes also allow ancestors from another region.
function scoped(selector: string, inside = false) {
  const at = selector.indexOf("::");
  const subject = at < 0 ? selector : selector.slice(0, at);
  const pseudo = at < 0 ? "" : selector.slice(at);
  return `:global(${subject}):where(.scope${inside ? " *" : ""})${pseudo}`;
}

// insertedBy tells a selector that names one of the classes run-time markup carries (by prefix), and no class that is
// neither that nor in the region: a rule for a part of the board the region does not have, whatever the engine inserts,
// is not kept.
const insertedBy = (prefixes: readonly string[], elements: Set<Element>) => {
  const present = new Set<string>();
  for (const element of elements)
    for (let ancestor: Element | null = element; ancestor; ancestor = ancestor.parentElement)
      for (const name of ancestor.classList) present.add(name);
  const carried = (name: string) => prefixes.some((prefix) => name.startsWith(prefix));
  return (selector: string) => {
    const named = requiredClasses(selector);
    return prefixes.length > 0 && named.some(carried) && named.every((name) => present.has(name) || carried(name));
  };
};

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

function matcher(elements: Set<Element>, states: readonly string[]) {
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
    const test = potential(selector, states);
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

function selectBlocks(
  blocks: CSSBlock[],
  matches: (selector: string) => boolean,
  isInserted: (selector: string) => boolean,
): string {
  return blocks
    .map(({ selector, body }) => {
      if (selector.startsWith("@keyframes")) return `${selector}{${body}}`;
      if (selector.startsWith("@")) {
        const inner = selectBlocks(cssBlocks(body), matches, isInserted);
        return inner ? `${selector}{${inner}}` : "";
      }
      // each selector is matched once: it applies to the region's own elements, or to run-time markup inside it
      const kept = selectors(selector).flatMap((one) => {
        if (matches(one)) return [scoped(one)];
        return isInserted(one) ? [scoped(one, true)] : [];
      });
      return kept.length ? `${kept.join(",")}{${body}}` : "";
    })
    .join("\n");
}

// regionCSS is the board's rules that can match the region: those its cut markup matches, and those naming a class
// prefix in `inserted`, for markup an engine puts into the region at run time, and those that only the run-time
// `states` (classes and attributes a script sets) keep from matching.
export function regionCSS(
  css: string,
  elements: Set<Element>,
  insertedPrefixes: readonly string[] = [],
  states: readonly string[] = [],
): string {
  const selected = selectBlocks(cssBlocks(css), matcher(elements, states), insertedBy(insertedPrefixes, elements));
  const withoutFrames = selected.replace(/@keyframes[\s\S]*?\}\s*\}/g, "");
  return cssBlocks(selected)
    .filter(({ selector }) => {
      const name = selector.match(/^@keyframes\s+(\S+)/)?.[1];
      return !name || new RegExp(`\\b${name}\\b`).test(withoutFrames);
    })
    .map(({ selector, body }) => `${selector}{${body}}`)
    .join("\n");
}
