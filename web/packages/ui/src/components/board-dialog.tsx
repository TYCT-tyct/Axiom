import { type ReactNode, type RefObject, useEffect } from "react";
import s from "./board-dialog.module.css";

// BoardDialog hosts a board-generated dialog region inside a native modal <dialog> so the browser traps Tab/Shift+Tab
// within it, handles Escape, and restores focus to the trigger control when closed, without altering the board's CSS.
export function BoardDialog({
  labelId,
  dialogRef,
  initialFocusRef,
  onClose,
  children,
}: {
  labelId: string;
  dialogRef: RefObject<HTMLDialogElement | null>;
  initialFocusRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (!dialog.open) dialog.showModal();
    initialFocusRef.current?.focus();
  }, [dialogRef, initialFocusRef]);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Escape is handled natively by modal <dialog>.
    <dialog
      ref={dialogRef}
      aria-labelledby={labelId}
      onClose={onClose}
      onClick={(event) => {
        const target = event.target;
        if (target === dialogRef.current || (target instanceof Element && target.classList.contains("nkscrim"))) {
          dialogRef.current?.close();
        }
      }}
      className={s.root}
    >
      {children}
    </dialog>
  );
}
