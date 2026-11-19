// The messages each page of an app can show, for the Chinese faces cut per page (text.ts): each module the app's router
// loads lazily is a page, which can show the keys the code it runs names, plus those of the shell every page of the app
// runs in (the entry and what it imports eagerly). Naming a key in code is what showing it takes, so a page cannot show
// a message outside its selection, short of a key assembled from pieces no literal spells; the selection is wider where
// a literal happens to match a key it does not show, which costs bytes, never a missing glyph. Packages are followed
// into their source through their package.json exports; anything outside the workspace is not.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import ts from "typescript";

const web = join(import.meta.dirname, "..", "..");

const workspacePackages = new Map(
  readdirSync(join(web, "packages")).map((dir) => {
    const pkg = JSON.parse(readFileSync(join(web, "packages", dir, "package.json"), "utf8"));
    return [pkg.name as string, { dir: join(web, "packages", dir), exports: pkg.exports as Record<string, string> }];
  }),
);
const isFile = (path: string) => existsSync(path) && statSync(path).isFile();

function resolveImport(from: string, specifier: string) {
  if (specifier.startsWith(".")) {
    const base = join(dirname(from), specifier);
    return ["", ".ts", ".tsx", "/index.ts", "/index.tsx"].map((ext) => base + ext).find(isFile);
  }
  const [, name = "", sub = ""] = /^(@[^/]+\/[^/]+)(\/.*)?$/.exec(specifier) ?? [];
  const pkg = workspacePackages.get(name);
  const target = pkg && (pkg.exports[`.${sub}`] ?? patternTarget(pkg.exports, `.${sub}`));
  const path = pkg && target && /\.tsx?$/.test(target) ? join(pkg.dir, target) : undefined;
  return path && isFile(path) ? path : undefined;
}

// patternTarget resolves a subpath through the package's "*" exports as Node does: the pattern with the longest prefix
// that matches, and of those the longest pattern ("./Login/regions/*.zh" before "./Login/regions/*" for a region's
// Chinese text), its "*" replaced by what the subpath has there.
export function patternTarget(exports: Record<string, string>, key: string) {
  const matches = Object.entries(exports)
    .map(([pattern, target]) => ({ pattern, target, star: pattern.indexOf("*") }))
    .filter(({ pattern, star }) => {
      const after = pattern.slice(star + 1);
      return (
        star >= 0 && key.startsWith(pattern.slice(0, star)) && key.endsWith(after) && key.length >= pattern.length - 1
      );
    })
    .sort((a, b) => b.star - a.star || b.pattern.length - a.pattern.length);
  const best = matches[0];
  return best?.target.replace("*", key.slice(best.star, key.length - (best.pattern.length - best.star - 1)));
}

// A module's string literals, and what it takes from others: an import names the exports it uses ("*" for all of them,
// none for a module run for its effects), a re-export forwards one name, and only what is used is followed, as the
// bundler drops the rest of a package's index. Type-only imports and exports run nothing and are skipped.
type Edge = { target: string; names: string[] };
type Module = { literals: string[]; imports: Edge[]; reexports: Map<string, Edge>; stars: string[]; lazy: string[] };
const parsed = new Map<string, Module>();

function importedNames(clause: ts.ImportClause | undefined) {
  const bindings = clause?.namedBindings;
  if (bindings && ts.isNamespaceImport(bindings)) {
    return ["*"];
  }
  const named = bindings
    ? bindings.elements.filter((e) => !e.isTypeOnly).map((e) => (e.propertyName ?? e.name).text)
    : [];
  return clause?.name ? ["default", ...named] : named;
}

function addExport(module: Module, node: ts.ExportDeclaration, target: string) {
  const clause = node.exportClause;
  if (!clause || ts.isNamespaceExport(clause)) {
    module.stars.push(target);
    return;
  }
  for (const e of clause.elements.filter((e) => !e.isTypeOnly)) {
    module.reexports.set(e.name.text, { target, names: [(e.propertyName ?? e.name).text] });
  }
}

function addDeclaration(module: Module, node: ts.ImportDeclaration | ts.ExportDeclaration, target: string) {
  if (ts.isImportDeclaration(node)) {
    node.importClause?.isTypeOnly || module.imports.push({ target, names: importedNames(node.importClause) });
  } else if (!node.isTypeOnly) {
    addExport(module, node, target);
  }
}

function parse(file: string): Module {
  const cached = parsed.get(file);
  if (cached) {
    return cached;
  }
  const module: Module = { literals: [], imports: [], reexports: new Map(), stars: [], lazy: [] };
  const target = (specifier: ts.Node | undefined) =>
    specifier && ts.isStringLiteral(specifier) ? resolveImport(file, specifier.text) : undefined;
  const visit = (node: ts.Node): void => {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) {
      const to = target(node.moduleSpecifier);
      to && addDeclaration(module, node, to);
      return;
    }
    if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword) {
      const to = target(node.arguments[0]);
      to && module.lazy.push(to);
      return;
    }
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isTemplateHead(node)) {
      module.literals.push(node.text);
    }
    ts.forEachChild(node, visit);
  };
  visit(ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest));
  parsed.set(file, module);
  return module;
}

type Walk = { lazy: boolean; seen: Set<string>; ran: Set<string> };

// forwarded returns where a module sends the uses of one of its names: its re-export of it, or else its star exports.
function forwarded(module: Module, name: string): Edge[] {
  if (name === "*") {
    return [...module.reexports.values(), ...module.stars.map((target) => ({ target, names: ["*"] }))];
  }
  const edge = module.reexports.get(name);
  return edge ? [edge] : module.stars.map((target) => ({ target, names: [name] }));
}

// run adds the modules that run when a module's names are used: its own, what they import, and with lazy also what
// they load later.
function run(file: string, names: string[], walk: Walk) {
  const module = parse(file);
  if (!walk.ran.has(file)) {
    walk.ran.add(file);
    const later = walk.lazy ? module.lazy.map((target) => ({ target, names: [] })) : [];
    for (const edge of [...module.imports, ...later]) {
      run(edge.target, edge.names, walk);
    }
  }
  for (const name of names.filter((name) => !walk.seen.has(`${file}#${name}`))) {
    walk.seen.add(`${file}#${name}`);
    for (const edge of forwarded(module, name)) {
      run(edge.target, edge.names, walk);
    }
  }
  return walk.ran;
}

const literalsOf = (files: Iterable<string>) => [...files].flatMap((file) => parse(file).literals);

// routeLiterals returns an app's pages ("console/pages/keys") and the string literals each runs, the shell's included.
export function routeLiterals(app: string, entry: string) {
  const shell = run(join(web, entry), [], { lazy: false, seen: new Set(), ran: new Set() });
  const pages = new Map<string, Set<string>>();
  for (const page of [...new Set([...shell].flatMap((file) => parse(file).lazy))].sort()) {
    const ran = run(page, ["default"], { lazy: true, seen: new Set(), ran: new Set() });
    const name = relative(join(web, app, "src"), page).replace(/\.tsx?$/, "");
    pages.set(`${app}/${name}`, new Set([...literalsOf(shell), ...literalsOf(ran)]));
  }
  return pages;
}

// names tells whether code naming these literals can show a key: a literal that is the key (or its plural forms' base),
// or that the key sits under when the literal has a dot ("settings.mail", which code completes as `${prefix}.save`) or
// is a template literal's fixed start ("codes."). A bare word is the key itself only: queries are named ["balance"],
// which is no reason to load every message under balance.
export function names(literals: Set<string>, key: string) {
  const base = key.replace(/_(zero|one|two|few|many|other)$/, "");
  if (literals.has(base)) {
    return true;
  }
  for (let at = base.lastIndexOf("."); at > 0; at = base.lastIndexOf(".", at - 1)) {
    const parent = base.slice(0, at);
    if (literals.has(`${parent}.`) || (parent.includes(".") && literals.has(parent))) {
      return true;
    }
  }
  return false;
}
