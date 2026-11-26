import { Slot } from "radix-ui";
import { cloneElement, type Key, type ReactElement, type ReactNode } from "react";
import { classes } from "../classes";
import { Icon } from "./icon";
import s from "./row-list.module.css";

export type RowItem = {
  key: Key;
  // A state dot at the start of the row; a row that is only a fact leaves it out.
  tone?: "ok" | "warn" | "bad" | "neutral";
  // Something else at the start: a rank, an avatar.
  lead?: ReactNode;
  title: ReactNode;
  sub?: ReactNode;
  // A value at the end of the row, before the arrow.
  end?: ReactNode;
  // A link the whole row becomes, given empty (a router link, or an anchor): the row's content goes inside it.
  link?: ReactElement<{ children?: ReactNode }>;
};

// RowList is the admin's list of rows inside a card (adlist and adli): a title with a line under it, a state dot or a
// number before, a figure after, and an arrow where the row leads somewhere. It lists what needs doing, what was
// deducted, who spent most. empty is said in the card when there are no rows. more is the button under a list that
// shows only the first of many ("再看 3 项").
export function RowList({
  label,
  items,
  empty,
  more,
  alignTop = false,
}: {
  label: string;
  items: readonly RowItem[];
  empty?: string;
  more?: { label: string; onPress: () => void };
  // A row's dot and arrow sit against its first line, for rows whose second line wraps (the dashboard's to-do).
  alignTop?: boolean;
}) {
  if (items.length === 0) {
    return empty ? <p className={s.none}>{empty}</p> : null;
  }
  return (
    <>
      <ul aria-label={label} className={classes(s.list, alignTop && s.top)}>
        {items.map((item) => (
          <li key={item.key}>
            <Row item={item} />
          </li>
        ))}
      </ul>
      {more ? (
        <button type="button" className={s.more} onClick={more.onPress}>
          {more.label}
        </button>
      ) : null}
    </>
  );
}

function Row({ item }: { item: RowItem }) {
  const content = (
    <>
      {item.tone ? <i className={classes(s.dot, s[item.tone])} aria-hidden="true" /> : null}
      {item.lead}
      <span className={s.text}>
        <b>{item.title}</b>
        {item.sub ? <span>{item.sub}</span> : null}
      </span>
      {item.end === undefined ? null : <div className={s.end}>{item.end}</div>}
      {item.link ? <Icon name="right" className={s.arrow} /> : null}
    </>
  );
  return item.link ? (
    <Slot.Root className={classes(s.row, s.link)}>{cloneElement(item.link, undefined, content)}</Slot.Root>
  ) : (
    <div className={s.row}>{content}</div>
  );
}
