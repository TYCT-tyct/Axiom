import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./switch.module.css";

// Switch turns one setting on or off at once (.sw2.qsw from AxApp/regions.css); a choice that waits for a save is a
// checkbox in a form instead. Its label names it for a screen reader, which also hears whether it is on. The hit area
// is 44px whatever its size.
export function Switch({
  label,
  checked,
  disabled = false,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className={classes(board.sw2, board.qsw, checked && board.on, s.root)}
      onClick={() => onChange(!checked)}
    >
      <i aria-hidden="true" />
    </button>
  );
}
