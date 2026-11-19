import { useId } from "react";
import s from "./document-text.module.css";

// The blocks a long document is made of: headings, paragraphs, lists, and a note set apart from the text (a warning
// that the text is a sample).
export type TextBlock =
  | { kind: "heading"; level: 1 | 2 | 3; text: string }
  | { kind: "paragraph"; text: string }
  | { kind: "note"; text: string }
  | { kind: "list"; items: string[] };

// DocumentText is a long document to read before agreeing to it: its title and a line under it (its version), then its
// text in a box of its own height that scrolls. The box is a region named by the title, in the tab order, as keyboard
// users can only scroll what takes focus. The text is React text, never markup.
export function DocumentText({ title, lede, blocks }: { title: string; lede?: string; blocks: TextBlock[] }) {
  const headingId = useId();
  return (
    <div className={s.root}>
      <h2 id={headingId} className={s.title}>
        {title}
      </h2>
      {lede ? <p className={s.lede}>{lede}</p> : null}
      {/* biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll the text. */}
      <section tabIndex={0} aria-label={title} className={s.body}>
        {blocks.map((block, index) => (
          <Block key={index} block={block} />
        ))}
      </section>
    </div>
  );
}

function Block({ block }: { block: TextBlock }) {
  if (block.kind === "heading") {
    return block.level === 1 ? <h3 className={s.h1}>{block.text}</h3> : <h4 className={s.h2}>{block.text}</h4>;
  }
  if (block.kind === "list") {
    return (
      <ul className={s.list}>
        {block.items.map((item, index) => (
          <li key={index}>{item}</li>
        ))}
      </ul>
    );
  }
  return <p className={block.kind === "note" ? s.note : s.paragraph}>{block.text}</p>;
}
