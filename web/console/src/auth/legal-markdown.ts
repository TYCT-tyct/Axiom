import type { TextBlock } from "@axiom/ui";

// Legal bodies are stored as Markdown headings, paragraphs, and lists. This reads that small subset into blocks of
// text (@axiom/ui DocumentText), never markup, so a document cannot inject any into the console. A sample document's
// first paragraph, which says it is one, is a note set apart from the text.
export function legalBlocks(body: string): TextBlock[] {
  const blocks: TextBlock[] = [];
  let paragraph: string[] = [];
  let list: string[] = [];

  const flushParagraph = () => {
    if (paragraph.length > 0) {
      blocks.push({ kind: "paragraph", text: paragraph.join(" ") });
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list.length > 0) {
      blocks.push({ kind: "list", items: list });
      list = [];
    }
  };

  for (const raw of body.replaceAll("\r\n", "\n").split("\n")) {
    const line = raw.trim();
    if (line === "") {
      flushParagraph();
      flushList();
      continue;
    }
    if (
      line.length >= 3 &&
      [...line].every((character) => character === "=" || character === "-" || character === "_")
    ) {
      flushParagraph();
      flushList();
      continue;
    }
    const heading = /^(#{1,3})\s+(.+?)\s*#*$/.exec(line);
    if (heading) {
      flushParagraph();
      flushList();
      blocks.push({
        kind: "heading",
        level: (heading[1]?.length ?? 1) as 1 | 2 | 3,
        text: heading[2] ?? "",
      });
      continue;
    }
    const item = /^[-*]\s+(.+)$/.exec(line);
    if (item) {
      flushParagraph();
      list.push(item[1] ?? "");
      continue;
    }
    flushList();
    paragraph.push(line);
  }
  flushParagraph();
  flushList();
  const [first] = blocks;
  if (first?.kind === "paragraph" && sample(first.text)) blocks[0] = { kind: "note", text: first.text };
  return blocks;
}

const sample = (text: string) => text.startsWith("【示例文本") || text.startsWith("Demo text, not legal advice");
