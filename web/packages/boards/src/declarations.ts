// Keep authored shorthand names: CSSOM enumeration expands background:var(--c0) into longhands whose values are
// empty until variable substitution. Reading those longhands loses the color when they are copied into React props.
export function declarationNames(text: string): string[] {
  const names: string[] = [];
  let start = 0;
  let depth = 0;
  const take = (end: number) => {
    const name = text
      .slice(start, end)
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .match(/^\s*([\w-]+)\s*:/)?.[1];
    if (name) names.push(name);
    start = end + 1;
  };
  for (const match of text.matchAll(/\/\*[\s\S]*?\*\/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[()[\]{};]/g)) {
    const token = match[0];
    if (token.length !== 1) continue;
    if ("([{".includes(token)) depth++;
    else if (")]}".includes(token)) depth--;
    else if (token === ";" && depth === 0) take(match.index);
  }
  take(text.length);
  return [...new Set(names)];
}
