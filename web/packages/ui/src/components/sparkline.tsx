import { classes } from "../classes";
import s from "./sparkline.module.css";

// Sparkline is the small trend beside a figure (the admin's adSpark): a line over the values scaled to their own
// range, ending in a dot that marks the latest value. The line stops short of the right edge so the dot has room. No
// values draw a flat rule. It is a picture of the figure beside it, so it is hidden from a screen reader unless it
// is given a label of its own.
export function Sparkline({
  values,
  tone = "data",
  width = 96,
  height = 24,
  label,
}: {
  values: readonly number[];
  tone?: "data" | "bad";
  width?: number;
  height?: number;
  label?: string;
}) {
  const lo = Math.min(...values);
  const span = Math.max(...values) - lo || 1;
  const at = (value: number, i: number) => [
    1 + (i * (width - 5)) / Math.max(1, values.length - 1),
    height - 3 - ((value - lo) / span) * (height - 6),
  ];
  const points = values.map((value, i) =>
    at(value, i)
      .map((n) => n.toFixed(1))
      .join(" "),
  );
  const [x, y] = at(values[values.length - 1] ?? lo, values.length - 1);
  const drawing = (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      aria-hidden="true"
      className={classes(s.root, s[tone])}
    >
      {values.length === 0 ? (
        <path className={s.rule} d={`M1 ${height - 2}H${width - 1}`} />
      ) : (
        <>
          <path className={s.line} d={`M${points.join("L")}`} />
          <circle className={s.end} cx={x?.toFixed(1)} cy={y?.toFixed(1)} r="2.5" />
        </>
      )}
    </svg>
  );
  return label ? (
    <span role="img" aria-label={label} className={s.named}>
      {drawing}
    </span>
  ) : (
    drawing
  );
}
