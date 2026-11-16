import { existsSync, readFileSync, writeFileSync } from "node:fs";

const sections = ["head", "template", "logic"];
const sectionFile = (file: string, section: string) => file.replace(/\.dc\.html$/, `.${section}.html`);

// The original AxApp exceeds the repository's 1 MB artifact limit. Text sections retain every byte and stay diffable.
export function readBoard(file: string): string {
  if (existsSync(file)) return readFileSync(file, "utf8");
  return sections.map((section) => readFileSync(sectionFile(file, section), "utf8")).join("");
}

export function pinBoard(file: string, html: string) {
  const head = html.indexOf("</helmet>") + "</helmet>".length;
  const body = html.indexOf("</x-dc>");
  const parts = [html.slice(0, head), html.slice(head, body), html.slice(body)];
  sections.forEach((section, index) => {
    writeFileSync(sectionFile(file, section), parts[index] as string);
  });
}
