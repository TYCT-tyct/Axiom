// Legal bodies are stored as Markdown headings, paragraphs, and lists. This renders that small subset as React text:
// raw HTML is never interpreted, so a document cannot inject markup into the console.
type LegalBlock =
  | { kind: "heading"; level: 1 | 2 | 3; text: string }
  | { kind: "paragraph"; text: string }
  | { kind: "list"; items: string[] };

function legalBlocks(body: string): LegalBlock[] {
  const blocks: LegalBlock[] = [];
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
  return blocks;
}

const documentClass =
  "max-h-56 overflow-y-auto p-4 text-ui leading-6 text-t3 [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2";

export function LegalMarkdown({ body, label }: { body: string; label: string }) {
  return (
    // biome-ignore format: keep the lint suppression and the tabIndex on one line.
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll the document.
    <section tabIndex={0} aria-label={label} className={documentClass}>
      {legalBlocks(body).map((block, index) => {
        if (block.kind === "heading") {
          if (block.level === 1) {
            return (
              <h3 key={index} className="mt-3 mb-2 text-ui font-medium text-t1 first:mt-0">
                {block.text}
              </h3>
            );
          }
          return (
            <h4 key={index} className="mt-4 mb-2 text-ui font-medium text-t2 first:mt-0">
              {block.text}
            </h4>
          );
        }
        if (block.kind === "list") {
          return (
            <ul key={index} className="my-2 list-disc space-y-1 pl-5">
              {block.items.map((item, itemIndex) => (
                <li key={itemIndex}>{item}</li>
              ))}
            </ul>
          );
        }
        const banner =
          index === 0 &&
          (block.text.startsWith("【示例文本") || block.text.startsWith("Demo text, not legal advice"));
        return (
          <p
            key={index}
            className={
              banner
                ? "mb-3 rounded-xs bg-warn-soft px-2 py-1 font-medium text-warn"
                : "my-2 whitespace-pre-line"
            }
          >
            {block.text}
          </p>
        );
      })}
    </section>
  );
}
