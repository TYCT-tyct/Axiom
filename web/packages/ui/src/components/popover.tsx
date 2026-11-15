import {
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { classes } from "../classes";
import s from "./popover.module.css";

export type TriggerProps = {
  ref: RefObject<HTMLButtonElement | null>;
  id: string;
  onClick: () => void;
  "aria-haspopup": "dialog" | "menu";
  "aria-expanded": boolean;
  "aria-controls"?: string;
};

// What opens the panel first when it appears: a menu's first item, or a popover's first control.
const focusable = '[role^="menuitem"], button:not(:disabled), [href], input:not(:disabled), select, textarea';

// Popover opens a panel under its trigger, a button the caller renders with the props it is given. The panel takes
// focus when it opens; Esc closes it and gives focus back to the trigger; a press outside or focus leaving it closes
// it. role is menu for Menu, dialog (non-modal) for any other content, which close() can also shut; label names the
// panel for a screen reader.
export function Popover({
  label,
  role = "dialog",
  align = "start",
  trigger,
  onPanelKeyDown,
  children,
}: {
  label: string;
  role?: "dialog" | "menu";
  align?: "start" | "end";
  trigger: (props: TriggerProps) => ReactNode;
  onPanelKeyDown?: (event: KeyboardEvent<HTMLDivElement>) => void;
  children: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const id = useId();
  const close = useCallback(() => {
    setOpen(false);
    button.current?.focus();
  }, []);
  useOutsidePress(open, root, () => setOpen(false));
  useEffect(() => {
    if (open) {
      panel.current?.querySelector<HTMLElement>(focusable)?.focus();
    }
  }, [open]);
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the keys and focus are the trigger's and the panel's, caught here.
    <div
      ref={root}
      className={s.root}
      onKeyDown={(event) => {
        // Esc closes the panel only, not a dialog around it.
        if (open && event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          close();
        }
      }}
      onBlur={(event) => {
        if (open && !root.current?.contains(event.relatedTarget as Node | null)) {
          setOpen(false);
        }
      }}
    >
      {trigger({
        ref: button,
        id: `${id}-trigger`,
        onClick: () => setOpen((was) => !was),
        "aria-haspopup": role,
        "aria-expanded": open,
        "aria-controls": open ? `${id}-panel` : undefined,
      })}
      {open ? (
        // biome-ignore lint/a11y/noStaticElementInteractions: the role is menu or dialog, both interactive.
        // biome-ignore lint/a11y/useAriaPropsSupportedByRole: menu and dialog both take a label.
        <div
          ref={panel}
          id={`${id}-panel`}
          role={role}
          aria-label={label}
          className={classes(s.panel, align === "end" && s.end)}
          onKeyDown={onPanelKeyDown}
        >
          {children(close)}
        </div>
      ) : null}
    </div>
  );
}

// useOutsidePress closes an open panel when the pointer goes down anywhere outside it and its trigger.
function useOutsidePress(open: boolean, root: RefObject<HTMLElement | null>, onOutside: () => void) {
  useEffect(() => {
    if (!open) {
      return;
    }
    const press = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) {
        onOutside();
      }
    };
    document.addEventListener("pointerdown", press);
    return () => document.removeEventListener("pointerdown", press);
  }, [open, root, onOutside]);
}
