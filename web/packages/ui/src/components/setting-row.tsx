import { type ReactNode, useEffect, useRef, useState } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./setting-row.module.css";

// SettingRow is one setting in a card of rows (.qrow/.qrl/.qrc from AxApp/regions.css): its name, what it does, and its
// control. The control names itself (a Switch's label, a Field's label), so the row's words are for reading, not a
// second label. A row that is a step of a list is its list item (as="li", inside a plain List).
//
// A row edited in place on a settings page (adRow in admin_set.js) passes dirty, and shows with the cyan dot that it
// holds a change nobody has submitted; reset undoes just that row and is live only while it is dirty. flash marks a row
// once when it is reached from search: it is scrolled to the middle and lit for a moment, and lights again only after
// flash has gone false and come back.
export function SettingRow({
  title,
  detail,
  as: Element = "div",
  dirty,
  reset,
  flash = false,
  children,
}: {
  title: string;
  detail?: ReactNode;
  as?: "div" | "li";
  dirty?: boolean;
  reset?: { label: string; name: string; onReset: () => void };
  flash?: boolean;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement & HTMLLIElement>(null);
  const edited = dirty !== undefined;
  const [lit, setLit] = useState(false);
  useEffect(() => {
    if (!flash) return;
    ref.current?.scrollIntoView?.({ block: "center" });
    setLit(true);
  }, [flash]);
  return (
    <Element
      ref={ref}
      className={classes(board.qrow, s.root, dirty && s.dirty, lit && s.flash)}
      // the flash is the row's own ::before; its children's animations end on themselves
      onAnimationEnd={(event) => event.target === event.currentTarget && setLit(false)}
    >
      <div className={classes(board.qrl, edited && s.words)}>
        <b className={classes(edited && s.title)}>
          {title}
          {edited ? <i className={s.mark} aria-hidden="true" /> : null}
          {reset ? (
            <button
              type="button"
              className={s.reset}
              aria-label={reset.name}
              tabIndex={dirty ? undefined : -1}
              aria-hidden={dirty ? undefined : true}
              onClick={reset.onReset}
            >
              {reset.label}
            </button>
          ) : null}
        </b>
        {detail ? <span className={s.detail}>{detail}</span> : null}
      </div>
      <div className={board.qrc}>{children}</div>
    </Element>
  );
}
