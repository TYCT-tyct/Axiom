import type { Emitter } from "./region-jsx.ts";
import { jsx } from "./region-jsx.ts";

function shape(node: Node): string {
  if (node.nodeType !== 1) return String(node.nodeType);
  const element = node as Element;
  return `${element.localName}[${[...element.attributes].map((a) => a.name).join(",")}](${[...node.childNodes].map(shape).join()})`;
}

function fields(node: Node): (Node | Attr)[] {
  if (node.nodeType === 3) return [node];
  if (node.nodeType !== 1) return [];
  return [
    ...[...(node as Element).attributes].filter(
      (a) => a.name !== "ref" && !a.name.startsWith("on") && !a.value.includes("{{"),
    ),
    ...[...node.childNodes].flatMap(fields),
  ];
}

function rowGroup(nodes: Element[], name: string, state: Emitter) {
  const variants = new Map<string, { kind: string; jsx: string; type: string }>();
  const demo: string[] = [];
  for (const [index, node] of nodes.entries()) {
    const signature = shape(node);
    const slots = fields(node);
    let variant = variants.get(signature);
    if (!variant) {
      const kind = String(variants.size);
      const mapping = new Map(slots.map((field, i) => [field, `item.f${i}`]));
      const type = `{ key: string; kind: ${JSON.stringify(kind)}; ${slots.map((_, i) => `f${i}: string;`).join(" ")} }`;
      variant = { kind, jsx: jsx(node, state, mapping), type };
      variants.set(signature, variant);
    }
    demo.push(
      `{ key: ${JSON.stringify(String(index))}, kind: ${JSON.stringify(variant.kind)}, ${slots.map((field, i) => `f${i}: ${state.literal(field.nodeValue ?? "")}`).join(",")} }`,
    );
  }
  const templates = [...variants.values()];
  const typeName = `${name[0]?.toUpperCase()}${name.slice(1)}Item`;
  const render = templates
    .map((variant, index) =>
      index === templates.length - 1
        ? variant.jsx
        : `item.kind === ${JSON.stringify(variant.kind)} ? ${variant.jsx} : `,
    )
    .join("");
  state.replacements.set(
    nodes[0] as Element,
    `{rows.${name}.map(item => <Fragment key={item.key}>{${render}}</Fragment>)}`,
  );
  for (const node of nodes.slice(1)) state.replacements.set(node, "");
  return {
    name,
    typeName,
    type: `type ${typeName} = ${templates.map((v) => v.type).join(" | ")};`,
    demo: `[${demo.join(",\n")}]`,
  };
}

export function rows(roots: Element[], state: Emitter) {
  const groups = [];
  for (const [name, selector] of Object.entries(state.region.repeats ?? {})) {
    const consecutive: Element[][] = [];
    for (const root of roots) {
      for (const node of root.querySelectorAll(selector)) {
        const group = consecutive.at(-1);
        if (group?.at(-1)?.nextElementSibling === node) group.push(node);
        else consecutive.push([node]);
      }
    }
    for (const [index, nodes] of consecutive.entries()) groups.push(rowGroup(nodes, `${name}${index}`, state));
  }
  return groups;
}
