import type { KeyboardEvent, ReactNode } from "react";
import { classes } from "../classes";
import { Icon, type IconName } from "./icon";
import s from "./menu.module.css";
import { Popover, type TriggerProps } from "./popover";

type MenuItem = {
  label: string;
  icon?: IconName;
  onSelect: () => void;
  danger?: boolean;
  // A menu of choices (a language, a workspace) marks the chosen one; a screen reader hears it as checked.
  checked?: boolean;
};

// Where each key moves the focus among the n items from the current one, wrapping at both ends.
const moves: Record<string, (at: number, n: number) => number> = {
  ArrowDown: (at, n) => (at + 1) % n,
  ArrowUp: (at, n) => (at - 1 + n) % n,
  Home: () => 0,
  End: (_, n) => n - 1,
};

function move(event: KeyboardEvent<HTMLDivElement>) {
  const step = moves[event.key];
  if (!step) {
    return;
  }
  event.preventDefault();
  const items = [...event.currentTarget.querySelectorAll<HTMLElement>('[role^="menuitem"]')];
  const at = items.indexOf(document.activeElement as HTMLElement);
  items[step(at, items.length)]?.focus();
}

// Menu is a menu button (WAI-ARIA APG): the trigger opens a list of actions with the first one focused; arrow keys,
// Home and End move among them; Enter or Space acts and closes it; Esc closes it and gives focus back to the trigger.
export function Menu({
  label,
  align,
  trigger,
  items,
}: {
  label: string;
  align?: "start" | "end";
  trigger: (props: TriggerProps) => ReactNode;
  items: readonly MenuItem[];
}) {
  return (
    <Popover label={label} role="menu" align={align} trigger={trigger} onPanelKeyDown={move}>
      {(close) =>
        items.map((item) => (
          // biome-ignore lint/a11y/useAriaPropsSupportedByRole: aria-checked is set only on a menuitemradio.
          <button
            key={item.label}
            type="button"
            role={item.checked === undefined ? "menuitem" : "menuitemradio"}
            aria-checked={item.checked}
            tabIndex={-1}
            className={classes(s.item, item.danger && s.danger)}
            onClick={() => {
              close();
              item.onSelect();
            }}
          >
            {item.icon ? (
              <span className={s.icon}>
                <Icon name={item.icon} />
              </span>
            ) : null}
            {item.label}
            {item.checked ? (
              <span className={s.check}>
                <Icon name="check" />
              </span>
            ) : null}
          </button>
        ))
      }
    </Popover>
  );
}
