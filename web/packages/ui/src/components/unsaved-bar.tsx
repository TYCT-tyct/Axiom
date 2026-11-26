import { type ReactNode, useEffect, useRef } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./unsaved-bar.module.css";

type BarAction = { label: string; onClick: () => void };

// UnsavedBar is the bar that rises at the bottom of a settings page while it holds changes nobody has submitted yet
// (adBar in admin_set.js): what is waiting ("3 项修改还没提交"), discard, and review-and-submit. It stays mounted and
// slides in and out with open; while closed it is inert, so neither focus nor a screen reader lands on it. Cmd or Ctrl
// with S submits while it is open and no dialog is: the shortcut hint shows it on a wide screen. A submit that cannot
// go (a field is wrong) is disabled, and the key does nothing either.
export function UnsavedBar({
  open,
  message,
  discard,
  submit,
  submitDisabled = false,
  shortcut,
}: {
  open: boolean;
  message: ReactNode;
  discard: BarAction;
  submit: BarAction;
  submitDisabled?: boolean;
  shortcut?: string;
}) {
  const send = useRef<(() => void) | undefined>(undefined);
  send.current = submitDisabled ? undefined : submit.onClick;
  useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "s") return;
      if (document.querySelector("dialog[open]")) return;
      event.preventDefault();
      send.current?.();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);
  return (
    <div className={classes(s.root, open && s.open)} inert={!open}>
      <div className={s.inner}>
        <span role="status" className={s.message}>
          {message}
        </span>
        <button type="button" className={classes(board.btn, board.gho, board.sm, s.discard)} onClick={discard.onClick}>
          {discard.label}
        </button>
        <button
          type="button"
          className={classes(board.btn, board.pri, board.sm, s.submit)}
          disabled={submitDisabled}
          aria-keyshortcuts={shortcut ? "Meta+S Control+S" : undefined}
          onClick={submit.onClick}
        >
          {submit.label}
          {shortcut ? <kbd className={s.shortcut}>{shortcut}</kbd> : null}
        </button>
      </div>
    </div>
  );
}
