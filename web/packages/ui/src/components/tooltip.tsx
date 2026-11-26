import { type ReactNode, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { classes } from "../classes";
import s from "./tooltip.module.css";

// How long a pointer rests on the trigger before the tip shows (the admin board's fxInit), so passing over a column
// of cells does not flash a tip at each one. Focus shows it at once: the keyboard has chosen.
const restMs = 400;
// The gap between the tip and its trigger, and the least room it keeps from the window's edges.
const gap = 6;

// Tooltip shows a short text over what it describes (the admin's data-tip): after a pointer rests on it, or at once
// when it takes focus, so a keyboard reaches every tip. Esc, leaving, a press or a scroll hides it. Without children it
// is a truncated cell (the admin's adEll): the text cut to the column with an ellipsis, the tip holding all of it. The
// tip is in the top layer (a manual popover), so a table's scroll box or a panel does not clip it; it sits above the
// trigger, or under it where the window has no room.
export function Tooltip({ text, children, width }: { text: string; children?: ReactNode; width?: number }) {
  const id = useId();
  const trigger = useRef<HTMLSpanElement>(null);
  const tip = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const timer = useRef(0);
  const hide = () => {
    window.clearTimeout(timer.current);
    setOpen(false);
  };
  useEffect(() => {
    if (!open) return;
    window.addEventListener("scroll", hide, { capture: true, passive: true });
    return () => window.removeEventListener("scroll", hide, { capture: true });
  }, [open]);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  useLayoutEffect(() => {
    const box = tip.current;
    const at = trigger.current?.getBoundingClientRect();
    if (!open || !box || !at) return;
    box.showPopover?.();
    const w = box.offsetWidth;
    const h = box.offsetHeight;
    const x = Math.max(gap, Math.min(window.innerWidth - w - gap, at.left + at.width / 2 - w / 2));
    const y = at.top - h - gap < gap ? at.bottom + gap : at.top - h - gap;
    box.style.setProperty("--x", `${Math.round(x)}px`);
    box.style.setProperty("--y", `${Math.round(y)}px`);
  }, [open]);
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the handlers only show and hide the tip; the trigger does nothing.
    <span
      ref={trigger}
      // biome-ignore lint/a11y/noNoninteractiveTabindex: the tip has to be reachable from the keyboard, so its trigger takes focus.
      tabIndex={0}
      className={classes(s.root, !children && s.clip)}
      style={width ? ({ "--w": `${width}px` } as Record<`--${string}`, string>) : undefined}
      aria-describedby={open && children ? id : undefined}
      onPointerEnter={() => {
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setOpen(true), restMs);
      }}
      onPointerLeave={hide}
      onPointerDown={hide}
      onFocus={() => setOpen(true)}
      onBlur={hide}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          event.stopPropagation();
          hide();
        }
      }}
    >
      {children ?? text}
      {open ? (
        <span ref={tip} id={id} role="tooltip" aria-hidden="true" popover="manual" className={s.tip}>
          {text}
        </span>
      ) : null}
    </span>
  );
}
