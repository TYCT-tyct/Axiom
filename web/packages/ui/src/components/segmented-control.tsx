import { useId } from "react";
import { Icon, type IconName } from "./icon";
import s from "./segmented-control.module.css";

// SegmentedControl is a choice among a few options, as radio buttons in a named group: arrow keys move the choice and
// a screen reader announces the group and the chosen option. An option shown as an icon keeps its words for a reader.
export function SegmentedControl<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: readonly { value: T; label: string; icon?: IconName }[];
  onChange: (value: T) => void;
}) {
  const name = useId();
  const chosen = Math.max(
    0,
    options.findIndex((option) => option.value === value),
  );
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={s.root}
      style={{ "--n": options.length, "--i": chosen } as Record<`--${string}`, number>}
    >
      <span aria-hidden="true" className={s.thumb} />
      {options.map((option) => (
        <label key={option.value} className={s.option} title={option.icon ? option.label : undefined}>
          <input
            type="radio"
            name={name}
            value={option.value}
            checked={option.value === value}
            onChange={() => onChange(option.value)}
            className={s.input}
          />
          {option.icon ? <Icon name={option.icon} /> : null}
          <span className={option.icon ? s.hidden : undefined}>{option.label}</span>
        </label>
      ))}
    </div>
  );
}
