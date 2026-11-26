import { classes } from "../classes";
import s from "./pace-bar.module.css";
import { Tooltip } from "./tooltip";

type Vars = Record<`--${string}`, string>;
const percent = (part: number, whole: number) => (whole > 0 ? Math.max(0, Math.min(100, (part / whole) * 100)) : 0);

// PaceBar is how much of a limit is used (the admin's adPace): a thin bar in the data colour that turns to the warning
// colour at the limit, or earlier when warn says so. Given where the use should be by now (a month's target spread over
// its days), it is the admin's goal bar instead, a full-width bar with a vertical mark at that point, whose tip says
// what the mark means. width sets the compact bar's length (110px by default, the board's).
export function PaceBar({
  label,
  value,
  max,
  warn = value >= max,
  width,
  expected,
  expectedLabel,
}: {
  label: string;
  value: number;
  max: number;
  warn?: boolean;
  width?: number;
  expected?: number;
  expectedLabel?: string;
}) {
  const goal = expected !== undefined;
  const p = `${percent(value, max)}%`;
  const bar = (
    <span
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={Math.min(value, max)}
      className={classes(s.root, goal ? s.goal : s.compact)}
      style={(width ? { "--p": p, "--w": `${width}px` } : { "--p": p }) as Vars}
    >
      <i className={classes(s.fill, warn && s.warn)} />
    </span>
  );
  if (!goal) return bar;
  // The mark is a sibling of the bar, not inside it: a progress bar's children are presentation, and the mark's tip
  // takes focus.
  return (
    <span className={s.frame}>
      {bar}
      <span className={s.mark} style={{ "--at": `${percent(expected, max)}%` } as Vars}>
        {expectedLabel ? (
          <Tooltip text={expectedLabel}>
            <span className={s.hit} />
          </Tooltip>
        ) : null}
      </span>
    </span>
  );
}
