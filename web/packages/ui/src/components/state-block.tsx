import type { ReactNode } from "react";
import { classes } from "../classes";
import { Glyph, type GlyphName } from "./glyph";
import { RequestId } from "./request-id";
import s from "./state-block.module.css";

// StateBlock stands in for content that is not there: empty, failed or not allowed (DataState picks which). A block
// that replaces the whole page has the page's level-one heading; one inside a section has a level-two one. compact is
// for a section, with less room around it.
export function StateBlock({
  icon,
  title,
  level,
  bad = false,
  compact = false,
  actions,
  requestId,
  children,
}: {
  icon: GlyphName;
  title: string;
  level: 1 | 2;
  bad?: boolean;
  compact?: boolean;
  actions?: ReactNode;
  requestId?: string | null;
  children?: ReactNode;
}) {
  const Heading = level === 1 ? "h1" : "h2";
  return (
    <div className={classes(s.root, compact && s.compact)}>
      <span className={classes(s.icon, bad && s.bad)}>
        <Glyph name={icon} size={20} />
      </span>
      <Heading className={s.title}>{title}</Heading>
      {children ? <div className={s.body}>{children}</div> : null}
      {actions ? <div className={s.actions}>{actions}</div> : null}
      {requestId ? (
        <span className={s.id}>
          <RequestId id={requestId} />
        </span>
      ) : null}
    </div>
  );
}

// Placeholder is loading: bars in the shape of the content to come, and the label as a status for a screen reader. A
// page that is loading still has its level-one heading, hidden because the bars say the same to a reader who looks,
// so a page waiting for its data can be told apart from one that never rendered (#363); it sits outside the status
// because it marks the page, it is not news.
export function Placeholder({ label, page = false }: { label: string; page?: boolean }) {
  return (
    <div className={s.placeholder}>
      {page ? <h1 className={s.unseen}>{label}</h1> : null}
      <div role="status" className={s.unseen}>
        {label}
      </div>
      {[0, 1, 2, 3, 4, 5, 6].map((bar) => (
        <span key={bar} aria-hidden="true" className={s.bar} />
      ))}
    </div>
  );
}
