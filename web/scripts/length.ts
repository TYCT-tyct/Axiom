// Fails when a function or a file is longer than the Go code may be (.golangci.yml: funlen 60 lines, file-length-limit
// 500), counting lines that hold code: blank and comment lines are skipped. Biome has rules for both, but they count no
// line inside JSX (Biome 2.5.15: a function returning 66 lines of JSX measured 3 lines), so a component of any length
// would pass them. Tests are exempt from the function limit, as in Go: a table or a scenario is long by nature.
import { globSync, readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";

const maxFunctionLines = 60;
const maxFileLines = 500;

const root = join(import.meta.dirname, "..");
// The files tsconfig.json and each package's tsconfig type-check; generated code is not written by hand.
const files = globSync(
  ["*.ts", "*/vite.config.ts", "scripts/**/*.ts", "e2e/**/*.ts", "{site,console,admin,packages/*}/src/**/*.{ts,tsx}"],
  { cwd: root, exclude: (path) => path.endsWith(".gen.ts") },
);
const isTest = (path: string) => /\.(test|spec)\.tsx?$/.test(path);

function codeLines(text: string) {
  return text.split("\n").filter((line) => line.trim() !== "" && !line.trim().startsWith("//")).length;
}

const failures: string[] = [];
for (const path of files) {
  const text = readFileSync(join(root, path), "utf8");
  if (codeLines(text) > maxFileLines) {
    failures.push(`${path}: ${codeLines(text)} lines, more than ${maxFileLines}`);
  }
  if (isTest(path)) {
    continue;
  }
  const source = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
  const visit = (node: ts.Node) => {
    if (ts.isFunctionLike(node) && "body" in node && node.body) {
      const { body } = node;
      // A block's braces sit on the lines of the signature and the end, as in funlen.
      const inner = ts.isBlock(body) ? 1 : 0;
      const lines = codeLines(text.slice(body.getStart(source) + inner, body.end - inner));
      if (lines > maxFunctionLines) {
        const { line } = source.getLineAndCharacterOfPosition(node.getStart(source));
        failures.push(`${path}:${line + 1}: a function of ${lines} lines, more than ${maxFunctionLines}`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
}

if (failures.length > 0) {
  console.error(`too long:\n${failures.join("\n")}`);
  process.exit(1);
}
