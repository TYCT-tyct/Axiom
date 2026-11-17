import ts from "typescript";

export function exportComponent(source: string): string {
  const tree = ts.createSourceFile("board.js", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS);
  const component = tree.statements.find((node) => ts.isClassDeclaration(node) && node.name?.text === "Component");
  if (!component) throw new Error("board has no top-level Component");
  const at = component.getStart(tree);
  return `${source.slice(0, at)}export ${source.slice(at)}`;
}

function assignment(node: ts.BinaryExpression, tree: ts.SourceFile): string | undefined {
  if (!ts.isPropertyAccessExpression(node.left)) return;
  const { left, right, operatorToken } = node;
  const property = left.name.text;
  const target = left.expression.getText(tree);
  const value = right.getText(tree);
  if (["innerHTML", "outerHTML"].includes(property) && operatorToken.kind === ts.SyntaxKind.EqualsToken) {
    return `boardHTML(${target}, ${value}, "${property === "outerHTML" ? "outer" : "inner"}")`;
  }
  if (property === "cssText")
    return `boardCSS(${target}, ${value}, ${operatorToken.kind === ts.SyntaxKind.PlusEqualsToken})`;
}

function call(node: ts.CallExpression, tree: ts.SourceFile): string | undefined {
  if (!ts.isPropertyAccessExpression(node.expression)) return;
  const target = node.expression.expression.getText(tree);
  if (node.expression.name.text === "insertAdjacentHTML")
    return `boardHTML(${target}, ${node.arguments[1]?.getText(tree)}, ${node.arguments[0]?.getText(tree)})`;
  const name = node.arguments[0];
  if (node.expression.name.text === "setAttribute" && name && ts.isStringLiteral(name) && name.text === "style")
    return `boardCSS(${target}.style, ${node.arguments[1]?.getText(tree)})`;
}

// Keep authored bytes except the DOM writes which would create CSP-blocked style attributes.
export function csp(source: string): string {
  const tree = ts.createSourceFile("board.js", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS);
  const edits: { start: number; end: number; text: string }[] = [];
  const visit = (node: ts.Node) => {
    const text = ts.isBinaryExpression(node)
      ? assignment(node, tree)
      : ts.isCallExpression(node)
        ? call(node, tree)
        : undefined;
    if (text) edits.push({ start: node.getStart(tree), end: node.end, text });
    else ts.forEachChild(node, visit);
  };
  visit(tree);
  for (const edit of edits.reverse()) source = source.slice(0, edit.start) + edit.text + source.slice(edit.end);
  return source;
}
