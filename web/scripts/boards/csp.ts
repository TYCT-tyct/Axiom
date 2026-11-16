import ts from "typescript";

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

function insertion(node: ts.CallExpression, tree: ts.SourceFile): string | undefined {
  if (!ts.isPropertyAccessExpression(node.expression) || node.expression.name.text !== "insertAdjacentHTML") return;
  return `boardHTML(${node.expression.expression.getText(tree)}, ${node.arguments[1]?.getText(tree)}, ${node.arguments[0]?.getText(tree)})`;
}

// Keep authored bytes except the DOM writes which would create CSP-blocked style attributes.
export function csp(source: string): string {
  const tree = ts.createSourceFile("board.js", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS);
  const edits: { start: number; end: number; text: string }[] = [];
  const visit = (node: ts.Node) => {
    const text = ts.isBinaryExpression(node)
      ? assignment(node, tree)
      : ts.isCallExpression(node)
        ? insertion(node, tree)
        : undefined;
    if (text) edits.push({ start: node.getStart(tree), end: node.end, text });
    else ts.forEachChild(node, visit);
  };
  visit(tree);
  for (const edit of edits.reverse()) source = source.slice(0, edit.start) + edit.text + source.slice(edit.end);
  return source;
}
