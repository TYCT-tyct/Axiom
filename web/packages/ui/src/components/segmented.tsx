import { useId } from "react";
import { cn } from "../cn";
import { Icon, type IconName } from "./icon";

export type SegmentedOption<T extends string> = { value: T; label: string; icon?: IconName };

// Segmented is a choice of a few options as a radio group, so arrow keys move the choice and a screen reader announces
// the group's name and the chosen option. An option shown only as an icon keeps its label for screen readers.
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
  className,
}: {
  label: string;
  value: T;
  options: SegmentedOption<T>[];
  onChange: (value: T) => void;
  className?: string;
}) {
  const name = useId();
  return (
    <fieldset className={cn("inline-flex gap-0.5 rounded-[calc(var(--r-sm)+3px)] bg-s2 p-[3px]", className)}>
      <legend className="sr-only">{label}</legend>
      {options.map((option) => (
        <label
          key={option.value}
          title={option.icon ? option.label : undefined}
          className="flex h-6.5 min-w-6.5 cursor-pointer items-center justify-center gap-1.5 rounded-sm px-2 text-ui text-t3 transition-colors duration-150 hover:text-t1 has-checked:bg-s4 has-checked:text-t1 has-focus-visible:outline-2 has-focus-visible:outline-accent has-focus-visible:outline-offset-2"
        >
          <input
            type="radio"
            name={name}
            value={option.value}
            checked={option.value === value}
            onChange={() => onChange(option.value)}
            className="sr-only"
          />
          {option.icon ? <Icon name={option.icon} /> : null}
          <span className={option.icon ? "sr-only" : undefined}>{option.label}</span>
        </label>
      ))}
    </fieldset>
  );
}
