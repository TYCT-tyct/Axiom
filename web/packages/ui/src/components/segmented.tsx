import { useId } from "react";
import { Icon, type IconName } from "./icon";
import s from "./segmented.module.css";

export type SegmentedOption<T extends string> = { value: T; label: string; icon?: IconName };

// Segmented is a choice of a few options as a radio group, so arrow keys move the choice and a screen reader announces
// the group's name and the chosen option. An option shown only as an icon keeps its label for screen readers.
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: SegmentedOption<T>[];
  onChange: (value: T) => void;
}) {
  const name = useId();
  return (
    <fieldset className={s.root}>
      <legend className={s.hidden}>{label}</legend>
      {options.map((option) => (
        <label key={option.value} title={option.icon ? option.label : undefined} className={s.option}>
          <input
            type="radio"
            name={name}
            value={option.value}
            checked={option.value === value}
            onChange={() => onChange(option.value)}
            className={s.hidden}
          />
          {option.icon ? <Icon name={option.icon} /> : null}
          <span className={option.icon ? s.hidden : undefined}>{option.label}</span>
        </label>
      ))}
    </fieldset>
  );
}
