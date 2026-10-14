// Fails when a function is more complex or longer, or a file longer, than the Go code may be (.golangci.yml: cyclop
// 12, funlen 60 lines, file-length-limit 500), counting lines that hold code: blank and comment lines are skipped.
// Biome scores cognitive complexity (biome.json) but has no cyclomatic rule, and its length rules count no line inside
// JSX (Biome 2.5.15: a function returning 66 lines of JSX measured 3 lines), so a component of any length would pass
// them. Tests are exempt from the function length limit, as in Go: a table or a scenario is long by nature.
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";

const maxComplexity = 12;
const maxFunctionLines = 60;
const maxFileLines = 500;

const isTest = (path: string) => /\.(test|spec)\.[jt]sx?$/.test(path);

// Each decision point adds a path, as cyclop counts if, for, case, && and ||; JavaScript adds its other loops, the
// conditional expression, catch and ??.
const branches = new Set([
  ts.SyntaxKind.IfStatement,
  ts.SyntaxKind.ConditionalExpression,
  ts.SyntaxKind.ForStatement,
  ts.SyntaxKind.ForInStatement,
  ts.SyntaxKind.ForOfStatement,
  ts.SyntaxKind.WhileStatement,
  ts.SyntaxKind.DoStatement,
  ts.SyntaxKind.CaseClause,
  ts.SyntaxKind.CatchClause,
]);
const shortCircuits = new Set([
  ts.SyntaxKind.AmpersandAmpersandToken,
  ts.SyntaxKind.BarBarToken,
  ts.SyntaxKind.QuestionQuestionToken,
  ts.SyntaxKind.AmpersandAmpersandEqualsToken,
  ts.SyntaxKind.BarBarEqualsToken,
  ts.SyntaxKind.QuestionQuestionEqualsToken,
]);

// A nested function is scored on its own, not added to the one around it: a component's callbacks are functions of
// their own, as in ESLint's complexity rule.
function complexity(body: ts.Node) {
  let paths = 1;
  const visit = (node: ts.Node) => {
    if (branches.has(node.kind) || (ts.isBinaryExpression(node) && shortCircuits.has(node.operatorToken.kind))) {
      paths++;
    }
    if (!ts.isFunctionLike(node)) {
      ts.forEachChild(node, visit);
    }
  };
  visit(body);
  return paths;
}

// The line of every token, once per line it spans with text on it. Comments are trivia, not tokens, so they mark no
// line, while string, template and JSX text do; JSDoc and a JSX expression holding only a comment are comments too.
function codeMarks(source: ts.SourceFile) {
  const marks: { pos: number; line: number }[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isJSDoc(node) || (ts.isJsxExpression(node) && !node.expression)) {
      return;
    }
    const children = node.getChildren(source);
    if (children.length > 0) {
      children.forEach(visit);
      return;
    }
    let pos = node.getStart(source);
    for (const part of source.text.slice(pos, node.end).split("\n")) {
      if (part.trim() !== "") {
        marks.push({ pos, line: source.getLineAndCharacterOfPosition(pos).line });
      }
      pos += part.length + 1;
    }
  };
  visit(source);
  return marks;
}

// The failures of one file, each as "path:line: reason".
export function check(path: string, text: string): string[] {
  const source = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
  const marks = codeMarks(source);
  const codeLines = (start: number, end: number) =>
    new Set(marks.filter(({ pos }) => pos >= start && pos < end).map(({ line }) => line)).size;

  const failures: string[] = [];
  const fileLines = codeLines(0, text.length);
  if (fileLines > maxFileLines) {
    failures.push(`${path}: ${fileLines} lines, more than ${maxFileLines}`);
  }
  const visit = (node: ts.Node) => {
    if (ts.isFunctionLike(node) && "body" in node && node.body) {
      const { body } = node;
      const at = `${path}:${source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1}`;
      const paths = complexity(body);
      if (paths > maxComplexity) {
        failures.push(`${at}: a function of complexity ${paths}, more than ${maxComplexity}`);
      }
      // A block's braces sit on the lines of the signature and the end, as in funlen.
      const inner = ts.isBlock(body) ? 1 : 0;
      const lines = codeLines(body.getStart(source) + inner, body.end - inner);
      if (lines > maxFunctionLines && !isTest(path)) {
        failures.push(`${at}: a function of ${lines} lines, more than ${maxFunctionLines}`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return failures;
}

if (import.meta.main) {
  const root = join(import.meta.dirname, "..");
  // Every script git does not ignore, tracked or new, is written by hand, except generated code.
  const files = execFileSync(
    "git",
    ["ls-files", "--cached", "--others", "--exclude-standard", "*.ts", "*.tsx", "*.js", "*.jsx", "*.mjs", "*.cjs"],
    { cwd: root, encoding: "utf8" },
  )
    .split("\n")
    .filter((path) => path !== "" && !path.endsWith(".gen.ts") && existsSync(join(root, path)));
  const failures = files.flatMap((path) => check(path, readFileSync(join(root, path), "utf8")));
  if (failures.length > 0) {
    console.error(`too complex or too long:\n${failures.join("\n")}`);
    process.exit(1);
  }
}
