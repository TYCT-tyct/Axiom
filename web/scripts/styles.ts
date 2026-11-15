// The style ownership rules of the redesign (docs/frontend.md「前端工程」, #747), as a ratchet. A page composes
// components and writes no style: app code passes no className and no style but custom properties, and imports no CSS.
// A component's styles live in one CSS Module beside it, imported only by it, whose selectors start at its own classes.
// Colours are written only in tokens.css. No source writes HTML or a style attribute as text, which the content
// security policy refuses (CSP3 §6.1.13, §6.1.15). A control's edge drawn 1px or wider stands out from every surface by
// 3:1 in both schemes (WCAG 2.2 1.4.11); a 0.5px line (--bw) is decoration.
//
// The current pages break the first rules many times. style-baseline.json records, per rule and file, how many breaks
// each file had when the rules came in: a file may not have more, a file that has fewer must lower its count in the same
// change (`node scripts/styles.ts --shrink`), so the number never sits above the truth, and a new file starts at 0. A
// path in `migrated` (a file or a directory ending in /) may have none, whatever the counts say.
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { contrast } from "../packages/ui/src/brand.ts";

type Rule =
  | "class-name"
  | "style"
  | "css-import"
  | "global-css"
  | "module-place"
  | "module-owner"
  | "selector"
  | "color"
  | "html"
  | "edge-contrast";
type Finding = { rule: Rule; path: string; line: number; message: string };
export type Baseline = { migrated: string[]; counts: Partial<Record<Rule, Record<string, number>>> };

const isTest = (path: string) => /\.(test|spec)\.[jt]sx?$/.test(path);
const isScript = (path: string) =>
  /\.[cm]?[jt]sx?$/.test(path) && /^(site|console|admin|packages\/[^/]+)\/src\//.test(path);
const isApp = (path: string) =>
  /^(console|admin|site|packages\/app)\/src\//.test(path) && !path.startsWith("site/src/home/");
const isModule = (path: string) => path.endsWith(".module.css");
const moduleHomes = [/^packages\/ui\/src\/components\//, /^site\/src\/home\//];
const globalCss = new Set([
  "packages/ui/src/css/tokens.css",
  "packages/ui/src/css/fonts.css",
  "packages/ui/src/css/base.css",
]);
const tokensPath = "packages/ui/src/css/tokens.css";

const lineOf = (text: string, pos: number) => text.slice(0, pos).split("\n").length;

// --- scripts -------------------------------------------------------------------------------------------------------

const htmlSinks = new Set(["innerHTML", "outerHTML", "insertAdjacentHTML", "cssText", "dangerouslySetInnerHTML"]);

// A className a component computes from its own module: any expression whose only strings are keys into the module.
function fromModule(node: ts.Node, module: string | undefined): boolean {
  if (ts.isStringLiteralLike(node) || ts.isTemplateExpression(node)) {
    return false;
  }
  // A comparison (size === "sm") chooses a class; its string is a value, not a class name.
  if (
    ts.isBinaryExpression(node) &&
    [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken].includes(
      node.operatorToken.kind,
    )
  ) {
    return true;
  }
  if (ts.isElementAccessExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === module) {
    return true;
  }
  let ok = true;
  ts.forEachChild(node, (child) => {
    ok &&= fromModule(child, module);
  });
  return ok && module !== undefined;
}

// A style object that sets only custom properties (--name), which a stylesheet reads, or none; anything else is inline
// style.
function customOnly(node: ts.Expression): boolean {
  if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node)) {
    return customOnly(node.expression);
  }
  if (ts.isConditionalExpression(node)) {
    return customOnly(node.whenTrue) && customOnly(node.whenFalse);
  }
  if (ts.isIdentifier(node)) {
    return node.text === "undefined";
  }
  return (
    ts.isObjectLiteralExpression(node) &&
    node.properties.every(
      (p) => ts.isPropertyAssignment(p) && ts.isStringLiteral(p.name) && p.name.text.startsWith("--"),
    )
  );
}

function scriptFindings(path: string, text: string): Finding[] {
  const source = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
  const found: Finding[] = [];
  const add = (rule: Rule, node: ts.Node, message: string) =>
    found.push({ rule, path, line: lineOf(text, node.getStart(source)), message });
  let module: string | undefined;
  const sibling = `./${path
    .split("/")
    .pop()
    ?.replace(/\.tsx$/, "")}.module.css`;
  const visit = (node: ts.Node) => {
    if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
      const from = node.moduleSpecifier.text;
      if (from === sibling && node.importClause?.name) {
        module = node.importClause.name.text;
      }
      if (isApp(path) && /\.css(\?.*)?$/.test(from)) {
        add("css-import", node, `app code imports CSS (${from}); a page composes components instead`);
      }
    }
    jsxFindings(node, module, add);
    htmlFindings(node, add);
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}

type Add = (rule: Rule, node: ts.Node, message: string) => void;

function jsxFindings(node: ts.Node, module: string | undefined, add: Add) {
  if (!ts.isJsxAttribute(node) || !ts.isIdentifier(node.name)) {
    return;
  }
  const value = node.initializer && ts.isJsxExpression(node.initializer) ? node.initializer.expression : undefined;
  if (node.name.text === "className" && !(value && fromModule(value, module))) {
    add("class-name", node, "className outside a component's own CSS Module; use a component's typed props");
  }
  if (node.name.text === "style" && !(value && customOnly(value))) {
    add("style", node, "style sets more than custom properties (--name)");
  }
}

// The member or attribute a node names: el.innerHTML, el["innerHTML"], <div dangerouslySetInnerHTML>.
function memberName(node: ts.Node) {
  if (ts.isPropertyAccessExpression(node)) {
    return node.name.text;
  }
  if (ts.isElementAccessExpression(node) && ts.isStringLiteral(node.argumentExpression)) {
    return node.argumentExpression.text;
  }
  return ts.isJsxAttribute(node) && ts.isIdentifier(node.name) ? node.name.text : undefined;
}

function htmlFindings(node: ts.Node, add: Add) {
  const name = memberName(node);
  if (name && htmlSinks.has(name)) {
    add("html", node, `${name} writes HTML or style as text, which the content security policy refuses`);
  }
  const [first] = ts.isCallExpression(node) ? node.arguments : [];
  if (
    ts.isCallExpression(node) &&
    memberName(node.expression) === "setAttribute" &&
    first &&
    ts.isStringLiteral(first) &&
    first.text.toLowerCase() === "style"
  ) {
    add("html", node, "setAttribute('style') writes a style attribute, which the content security policy refuses");
  }
}

// --- stylesheets ---------------------------------------------------------------------------------------------------

// at is where the prelude starts and open where its body does, both offsets into the whole stylesheet.
type Block = { prelude: string; body: string; at: number; open: number };

// The offset just past the brace that closes the one at open.
function closing(text: string, open: number) {
  let depth = 0;
  let end = open;
  do {
    depth += text[end] === "{" ? 1 : text[end] === "}" ? -1 : 0;
    end++;
  } while (depth > 0 && end < text.length);
  return end;
}

// The top-level blocks of a stylesheet (comments blanked so offsets keep their lines), with the offset of each.
function blocks(text: string, offset = 0): Block[] {
  const out: Block[] = [];
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    if (text[i] === ";" && !text.slice(start, i).includes("{")) {
      start = i + 1;
    } else if (text[i] === "{") {
      const end = closing(text, i);
      const prelude = text.slice(start, i);
      out.push({
        prelude: prelude.trim(),
        body: text.slice(i + 1, end - 1),
        at: offset + start + prelude.search(/\S/),
        open: offset + i + 1,
      });
      start = end;
      i = end - 1;
    }
  }
  return out;
}

// Every style rule of a stylesheet, inside @media, @supports, @container and @layer. A keyframe (from, to, 50%) is a
// rule too when keyframes is set: it holds declarations but no selector.
function styleRules(text: string, offset = 0, keyframes = false): Block[] {
  return blocks(text, offset).flatMap((block) => {
    if (!block.prelude.startsWith("@")) {
      return [block];
    }
    if (/^@(media|supports|container|layer)\b/.test(block.prelude)) {
      return styleRules(block.body, block.open, keyframes);
    }
    return keyframes && block.prelude.startsWith("@keyframes") ? blocks(block.body, block.open) : [];
  });
}

// Splits at the separators outside parentheses and brackets: commas of a selector list, or of shadow layers.
function splitTop(text: string, separator: RegExp) {
  const parts: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i] ?? "";
    depth += "([".includes(c) ? 1 : ")]".includes(c) ? -1 : 0;
    if (depth === 0 && separator.test(c)) {
      parts.push(text.slice(start, i));
      start = i + 1;
    }
  }
  return [...parts, text.slice(start)].map((part) => part.trim()).filter(Boolean);
}

const globalContext = /^:global\(((?::root|\[data-(?:theme|neutral)(?:="[\w-]+")?\])+)\)\s*/;

// A selector stays inside its component when, after an optional global context of the root's theme or neutral scale,
// its first compound names one of the module's own classes; ":global" is allowed nowhere else.
function anchored(selector: string) {
  const rest = selector.replace(globalContext, "");
  if (rest.includes(":global")) {
    return false;
  }
  const first = splitTop(rest, /[\s>+~]/)[0] ?? "";
  return /^[^(]*?\.[\w-]/.test(first.replace(/\([^)]*\)/g, ""));
}

// A module is flat: a rule inside a rule, or an at-rule the check does not read (@scope, @starting-style), could hide a
// selector from it, so neither is allowed.
function structureFindings(path: string, text: string, body = text, offset = 0): Finding[] {
  return blocks(body, offset).flatMap((block) => {
    const at = (message: string) => [{ rule: "selector" as const, path, line: lineOf(text, block.at), message }];
    if (block.prelude.startsWith("@keyframes")) {
      return [];
    }
    if (/^@(media|supports|container|layer)\b/.test(block.prelude)) {
      return structureFindings(path, text, block.body, block.open);
    }
    if (block.prelude.startsWith("@")) {
      return at(
        `"${block.prelude}" is an at-rule this check does not read; a module uses @media, @supports, @container`,
      );
    }
    return block.body.includes("{") ? at(`"${block.prelude}" nests a rule; a module writes its rules flat`) : [];
  });
}

function selectorFindings(path: string, text: string): Finding[] {
  return [
    ...structureFindings(path, text),
    ...styleRules(text).flatMap((rule) => ruleSelectorFindings(path, text, rule)),
  ];
}

function ruleSelectorFindings(path: string, text: string, rule: Block): Finding[] {
  return splitTop(rule.prelude, /,/)
    .filter((selector) => !anchored(selector))
    .map((selector) => ({
      rule: "selector" as const,
      path,
      line: lineOf(text, rule.at),
      message: `"${selector}" does not start at one of this module's classes`,
    }));
}

const namedColors =
  "aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen";
const colorLiteral = new RegExp(
  `#[0-9a-f]{3,8}\\b|\\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\\(|\\b(?:${namedColors.replaceAll(" ", "|")})\\b(?![-(])`,
  "i",
);

type Declaration = { property: string; value: string; at: number };

// Every declaration of a stylesheet, custom properties and keyframes included.
function declarations(text: string): Declaration[] {
  return styleRules(text, 0, true).flatMap((rule) => {
    return [...rule.body.matchAll(/([\w-]+)\s*:\s*([^;]+)/g)].map((m) => ({
      property: m[1] ?? "",
      value: (m[2] ?? "").trim(),
      at: rule.open + (m.index ?? 0),
    }));
  });
}

function colorFindings(path: string, text: string): Finding[] {
  return declarations(text)
    .filter(({ property, value }) => {
      // A var() keeps its fallback, which can be a literal; names and strings go.
      const bare = value.replace(/var\(\s*--[\w-]+\s*,?|--[\w-]+|"[^"]*"|'[^']*'/g, " ");
      return property !== "font-family" && colorLiteral.test(bare);
    })
    .map(({ value, at }) => ({
      rule: "color" as const,
      path,
      line: lineOf(text, at),
      message: `colour literal in "${value}"; colours are written only in tokens.css`,
    }));
}

// --- contrast of control edges -------------------------------------------------------------------------------------

type Rgb = [number, number, number];
type Scheme = { dark: boolean; neutral: Record<string, string> };

function tokens(css: string) {
  const read = (selector: string) => {
    const block = css.split(`${selector} {`)[1]?.split("\n}")[0] ?? "";
    return Object.fromEntries([...block.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2]?.trim() ?? ""]));
  };
  const base = read(":root");
  const slate = { ...base, ...read(':root[data-neutral="slate"]') };
  const schemes: Scheme[] = [base, slate].flatMap((neutral) => [false, true].map((dark) => ({ dark, neutral })));
  // A token as #rrggbb in one scheme, following var() and taking light-dark()'s side; undefined when it is not one
  // colour (a shadow, a mix).
  const color = (name: string, { dark, neutral }: Scheme): Rgb | undefined => {
    const pair = /^light-dark\((.+?),\s*(.+)\)$/.exec(neutral[name] ?? "");
    const text = (pair ? (dark ? pair[2] : pair[1]) : neutral[name])?.trim() ?? "";
    const ref = /^var\((--[\w-]+)\)$/.exec(text)?.[1];
    if (ref) {
      return color(ref, { dark, neutral });
    }
    return /^#[0-9a-f]{6}$/i.test(text)
      ? ([1, 3, 5].map((i) => Number.parseInt(text.slice(i, i + 2), 16)) as Rgb)
      : undefined;
  };
  return { schemes, color, raw: base };
}

const edges = /^(border(-(top|right|bottom|left|block|inline)(-(start|end))?)?(-color)?|outline(-color)?|box-shadow)$/;
const surfaces = ["--n1", "--n2", "--n3", "--n4"];
type Palette = ReturnType<typeof tokens>;

// The weakest contrast of a token against the surfaces a control sits on, over both schemes and neutral scales;
// undefined when the token is not one colour in every scheme (a mix, a shadow, a name nothing defines), which cannot
// be measured.
function weakest(name: string, palette: Palette) {
  let low = Number.POSITIVE_INFINITY;
  for (const scheme of palette.schemes) {
    const edge = palette.color(name, scheme);
    if (!edge) {
      return undefined;
    }
    for (const surface of surfaces) {
      const under = palette.color(surface, scheme);
      low = under ? Math.min(low, contrast(edge, under)) : low;
    }
  }
  return low;
}

// The edges a value draws, with the module's own custom properties put in for every value the module gives them and
// a layer that is only a shadow token (--sh1) put in by the token's value, so an alias is measured as what it stands
// for.
function expand(value: string, local: Map<string, string[]>, palette: Palette, depth = 0): string[] {
  const alias = [...value.matchAll(/var\((--[\w-]+)\)/g)].map((m) => m[1] ?? "").find((name) => local.has(name));
  if (alias && depth < 8) {
    return (local.get(alias) ?? []).flatMap((v) =>
      expand(value.replaceAll(`var(${alias})`, v), local, palette, depth + 1),
    );
  }
  return splitTop(value, /,/).flatMap((layer) => {
    const only = /^var\((--[\w-]+)\)$/.exec(layer)?.[1] ?? "";
    const shadow = palette.raw[only];
    return shadow && /\d(px)?\s/.test(shadow) && depth < 8 ? expand(shadow, local, palette, depth + 1) : [layer];
  });
}

const isLength = (part: string) => /^-?[\d.]+(px|r?em)?$/.test(part) || part.startsWith("calc(");
const lineStyles = new Set(["solid", "dashed", "dotted", "double", "groove", "ridge", "inset", "outset", "hidden"]);

// A layer draws no edge when it is none or 0, decoration when drawn --bw wide, and a cast shadow, not an edge, when it
// is blurred: a shadow's third length is its blur.
function isEdge(layer: string) {
  const lengths = splitTop(layer, /\s/).filter(isLength);
  const blurred = lengths.length >= 3 && Number.parseFloat(lengths[2] ?? "0") !== 0;
  return !/^(none|0)$/.test(layer) && !layer.includes("--bw") && !blurred;
}

// The colour a layer draws: what is left of it without its lengths and line style; "" when it names none, which draws
// in the text colour.
const edgeColor = (layer: string) =>
  splitTop(layer, /\s/)
    .filter((part) => !isLength(part) && !lineStyles.has(part))
    .join(" ");

// The colour of an edge is measured as drawn: it is one colour token or nothing visible (transparent). A mix, the text
// colour or a name nothing defines cannot be measured, whatever tokens it is made of, so it fails.
function edgeFindings(path: string, text: string, palette: Palette): Finding[] {
  const all = declarations(text);
  const local = new Map<string, string[]>();
  for (const { property, value } of all.filter((d) => d.property.startsWith("--"))) {
    local.set(property, [...(local.get(property) ?? []), value]);
  }
  return all
    .filter(({ property }) => edges.test(property))
    .flatMap(({ value, at }) =>
      expand(value, local, palette)
        .filter(isEdge)
        .map(edgeColor)
        .filter((color) => color !== "transparent")
        .map((color) => {
          const name = /^var\((--[\w-]+)\)$/.exec(color)?.[1];
          return { color: color || "currentColor", low: name ? weakest(name, palette) : undefined };
        })
        .filter(({ low }) => low === undefined || low < 3)
        .map(({ color, low }) => ({
          rule: "edge-contrast" as const,
          path,
          line: lineOf(text, at),
          message:
            low === undefined
              ? `edge in ${color}, which is not one colour token, so its contrast cannot be measured; use --ctl-line`
              : `edge in ${color} is ${low.toFixed(2)}:1 on a surface, under 3:1; use --ctl-line, or --bw if decoration`,
        })),
    );
}

// --- the whole tree ------------------------------------------------------------------------------------------------

function cssFindings(path: string, text: string, palette: ReturnType<typeof tokens>): Finding[] {
  const found = path === tokensPath ? [] : colorFindings(path, text);
  if (!isModule(path)) {
    return globalCss.has(path)
      ? found
      : [
          ...found,
          { rule: "global-css", path, line: 1, message: "global CSS outside tokens.css, fonts.css, base.css" },
        ];
  }
  if (!moduleHomes.some((home) => home.test(path))) {
    found.push({
      rule: "module-place",
      path,
      line: 1,
      message: "a CSS Module outside packages/ui/src/components/ and site/src/home/",
    });
  }
  return [...found, ...selectorFindings(path, text), ...edgeFindings(path, text, palette)];
}

// Every import of a CSS Module for its classes, by a relative path; a test reading one's text (?raw) applies no style
// and gets no class.
function moduleImports(files: Record<string, string>) {
  return Object.entries(files)
    .filter(([path]) => isScript(path))
    .flatMap(([path, text]) =>
      [...text.matchAll(/from\s+["'](\.{1,2}\/[^"']+\.module\.css)["']/g)].map((m) => ({
        path,
        text,
        spec: m[1] ?? "",
        at: m.index ?? 0,
      })),
    );
}

// Each module is imported by the one TSX beside it with its name, and by nothing else.
function ownerFindings(files: Record<string, string>): Finding[] {
  const found: Finding[] = [];
  const owned = new Set<string>();
  for (const { path, text, spec, at } of moduleImports(files)) {
    const target = join(path, "..", spec);
    if (target === path.replace(/\.tsx$/, ".module.css")) {
      owned.add(target);
    } else {
      const message = `imports ${spec}, which only ${target.replace(/\.module\.css$/, ".tsx")} may import`;
      found.push({ rule: "module-owner", path, line: lineOf(text, at), message });
    }
  }
  for (const path of Object.keys(files).filter((p) => isModule(p) && !owned.has(p))) {
    found.push({ rule: "module-owner", path, line: 1, message: "no TSX of the same name beside it imports it" });
  }
  return found;
}

function findings(files: Record<string, string>): Finding[] {
  const palette = tokens(files[tokensPath] ?? "");
  return [
    ...Object.entries(files).flatMap(([path, text]) => {
      if (path.endsWith(".css")) {
        return cssFindings(
          path,
          text.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " ")),
          palette,
        );
      }
      return isScript(path) && !isTest(path) ? scriptFindings(path, text) : [];
    }),
    ...ownerFindings(files),
  ];
}

function counts(found: Finding[]): Baseline["counts"] {
  const out: Baseline["counts"] = {};
  // Sorted, so the baseline written from it reads in a stable order.
  const sorted = [...found].sort((a, b) => a.rule.localeCompare(b.rule) || a.path.localeCompare(b.path));
  for (const { rule, path } of sorted) {
    const files = out[rule] ?? {};
    files[path] = (files[path] ?? 0) + 1;
    out[rule] = files;
  }
  return out;
}

const inMigrated = (path: string, migrated: string[]) =>
  migrated.some((m) => (m.endsWith("/") ? path.startsWith(m) : path === m));

// One file's failures under one rule: more breaks than it had, or fewer without the baseline coming down with them.
function ratchet(here: Finding[], path: string, rule: Rule, was: number, migrated: boolean) {
  const allowed = migrated ? 0 : was;
  if (here.length > allowed && allowed === 0) {
    return here.map((f) => `${f.path}:${f.line}: ${rule}: ${f.message}`);
  }
  // A count says that a file grew, not which of its uses is the new one, so all of them are named.
  if (here.length > allowed) {
    const lines = here.map((f) => f.line).join(", ");
    return [
      `${path}:${here[here.length - 1]?.line}: ${rule}: ${here[0]?.message}; ${here.length} here, ${allowed} allowed (lines ${lines})`,
    ];
  }
  if (here.length < was) {
    return [`${path}: ${rule}: ${here.length} left of ${was}; lower it in scripts/style-baseline.json (--shrink)`];
  }
  return [];
}

// The failures against the baseline, each as "path:line: rule: reason" or "path: rule: reason".
export function check(files: Record<string, string>, baseline: Baseline): string[] {
  const found = findings(files);
  const now = counts(found);
  const failures: string[] = [];
  const rules = new Set([...Object.keys(now), ...Object.keys(baseline.counts)] as Rule[]);
  for (const rule of rules) {
    const was = baseline.counts[rule] ?? {};
    const is = now[rule] ?? {};
    for (const path of new Set([...Object.keys(was), ...Object.keys(is)])) {
      const here = found.filter((f) => f.rule === rule && f.path === path);
      failures.push(...ratchet(here, path, rule, was[path] ?? 0, inMigrated(path, baseline.migrated)));
    }
  }
  return failures;
}

// Every file git does not ignore under the apps and packages, tracked or new, with its text.
export function tree(root: string): Record<string, string> {
  const paths = execFileSync(
    "git",
    ["ls-files", "--cached", "--others", "--exclude-standard", "*.css", "*.ts", "*.tsx", "*.js", "*.jsx"],
    {
      cwd: root,
      encoding: "utf8",
    },
  )
    .split("\n")
    .filter((path) => (isScript(path) || path.endsWith(".css")) && existsSync(join(root, path)));
  return Object.fromEntries(paths.map((path) => [path, readFileSync(join(root, path), "utf8")]));
}

export const baselinePath = join(import.meta.dirname, "style-baseline.json");

if (import.meta.main) {
  const root = join(import.meta.dirname, "..");
  const baseline: Baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
  const files = tree(root);
  const failures = check(files, baseline);
  const grown = failures.filter((f) => !f.includes("--shrink"));
  if (process.argv.includes("--shrink") && grown.length === 0) {
    writeFileSync(baselinePath, `${JSON.stringify({ ...baseline, counts: counts(findings(files)) }, null, 2)}\n`);
  } else if (failures.length > 0) {
    console.error(`style rules (docs/frontend.md「前端工程」):\n${failures.join("\n")}`);
    process.exit(1);
  }
}
