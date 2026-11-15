import { type KeyboardEvent, type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { scaleAmount, useFormat } from "../format";
import s from "./chart.module.css";

// The charts of the usage pages, drawn in SVG: stacked bars and lines over time. The shapes are stretched to the width
// (preserveAspectRatio none) so they stay one drawing at any width; every word (axis ticks, the tooltip, the legend)
// is HTML beside them, so it stays sharp and a user's fonts and language apply.
type Series = { key: string; label: string };
// One time slot: its values by series key. A key it lacks is 0; a negative or non-finite value is drawn as 0, since a
// usage chart has no such value and the scale must stay finite.
type Point = { at: Date; values: Readonly<Record<string, number>> };
// What a value counts: whole things (requests, tokens), or money in a currency, shown through format.ts.
type Unit = "count" | { currency: string };
// How coarse the slots are, which sets how the axis names them.
type Step = "hour" | "day";

type ChartProps = {
  label: string;
  // Said over the plot when there are no points.
  empty: string;
  series: readonly Series[];
  points: readonly Point[];
  unit: Unit;
  step: Step;
  // The zone the slots are named in; the viewer's own when absent.
  timeZone?: string;
};

const plotHeight = 190;
const plotWidth = 1000;
const ticks = 3;
const maxLabels = 6;
const maxValue = 1e15;
const colours = 5;
const maxBar = 64;

type Vars = Record<`--${string}`, number>;

const clean = (value: number | undefined) =>
  value !== undefined && Number.isFinite(value) && value > 0 ? Math.min(value, maxValue) : 0;
const colour = (order: number) => s[`s${order % colours}`];

// niceStep rounds a step up to 1, 2, 5 times a power of ten, so the gridlines fall on round numbers.
function niceStep(raw: number) {
  const power = 10 ** Math.floor(Math.log10(raw));
  const lead = raw / power;
  return (lead <= 1 ? 1 : lead <= 2 ? 2 : lead <= 5 ? 5 : 10) * power;
}

// The scale: `ticks` gridlines at round steps, the top one at or above the largest value. Nothing to draw, or a count of
// less than three, still gets a step of one, so the axis never reads 0.5 requests or a flat line at zero.
function scaleOf(max: number, unit: Unit) {
  const raw = max > 0 ? niceStep(max / ticks) : 1;
  const step = unit === "count" ? Math.max(raw, 1) : raw;
  return { step, top: step * ticks };
}

// The words of a chart, in the page's language and time zone: a value as a count or as money, a slot named in full for
// the tooltip and the table, and short for the axis.
function useChartWords(unit: Unit, step: Step, timeZone?: string) {
  const format = useFormat(timeZone);
  return {
    amount: (value: number) =>
      unit === "count" ? format.number(value) : format.money({ amount: scaleAmount(value), currency: unit.currency }),
    slot: (at: Date) => (step === "hour" ? format.dateTimeMinute(at) : format.dayFull(at)),
    tick: (at: Date) => (step === "hour" ? format.hour(at) : format.day(at)),
  };
}
type Words = ReturnType<typeof useChartWords>;

// How the plot's moves are made from the keys: Left and Right step one slot (the first press lands on an end), Home and
// End jump, Esc clears.
function moveActive(key: string, active: number | null, n: number) {
  const to: Record<string, number> = {
    ArrowRight: Math.min((active ?? -1) + 1, n - 1),
    ArrowLeft: Math.max((active ?? n) - 1, 0),
    Home: 0,
    End: n - 1,
  };
  return n > 0 ? to[key] : undefined;
}

// Bars stack in series order from the bottom, each slot's bar at most maxBar units wide so one point is a bar, not a wall.
function Bars({ series, points, top }: { series: readonly Series[]; points: readonly Point[]; top: number }) {
  const slot = plotWidth / points.length;
  const width = Math.min(slot * 0.62, maxBar);
  return (
    <g>
      {points.map((point, at) => {
        let floor = plotHeight;
        return series.map((one, order) => {
          const height = (Math.min(clean(point.values[one.key]), top) / top) * plotHeight;
          floor -= height;
          return height > 0 ? (
            <rect
              key={`${point.at.getTime()}-${one.key}`}
              className={classes(s.bar, colour(order))}
              x={slot * at + (slot - width) / 2}
              y={floor}
              width={width}
              height={height}
              rx="2"
            />
          ) : null;
        });
      })}
    </g>
  );
}

// A line per series through the slots' middles; one point is a dot (a zero-length round-capped segment keeps its size
// when the drawing is stretched, which a circle would not). The active slot gets a dot on every line.
function Lines({
  series,
  points,
  top,
  active,
}: {
  series: readonly Series[];
  points: readonly Point[];
  top: number;
  active: number | null;
}) {
  const slot = plotWidth / points.length;
  return (
    <g>
      {series.map((one, order) => {
        const at = (index: number) =>
          `${slot * (index + 0.5)},${plotHeight - (Math.min(clean(points[index]?.values[one.key]), top) / top) * plotHeight}`;
        const path = points.length === 1 ? `M${at(0)}h0` : `M${points.map((_, index) => at(index)).join("L")}`;
        return (
          <g key={one.key} className={colour(order)}>
            <path className={s.line} d={path} />
            {active === null ? null : <path className={s.dot} d={`M${at(active)}h0`} />}
          </g>
        );
      })}
    </g>
  );
}

function Tip({ point, x, series, words }: { point: Point; x: number; series: readonly Series[]; words: Words }) {
  return (
    <div className={s.tip} aria-hidden="true" style={{ "--x": x } as Vars}>
      <b>{words.slot(point.at)}</b>
      {series.map((one, order) => (
        <span key={one.key}>
          <i className={colour(order)} />
          {one.label}
          <b>{words.amount(clean(point.values[one.key]))}</b>
        </span>
      ))}
    </div>
  );
}

// The same numbers as the plot, for a screen reader: a table, drawn nowhere.
function ChartTable({
  label,
  series,
  points,
  words,
}: Pick<ChartProps, "label" | "series" | "points"> & { words: Words }) {
  const { t } = useTranslation("common");
  return (
    <table className={s.sr}>
      <caption>{label}</caption>
      <thead>
        <tr>
          <th scope="col">{t("chart.time")}</th>
          {series.map((one) => (
            <th key={one.key} scope="col">
              {one.label}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {points.map((point) => (
          <tr key={point.at.getTime()}>
            <th scope="row">{words.slot(point.at)}</th>
            {series.map((one) => (
              <td key={one.key}>{words.amount(clean(point.values[one.key]))}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// useActiveSlot is which slot a pointer or a key has made active, and the handlers that move it: Left and Right step one
// slot (the first press lands on an end), Home and End jump, Esc clears. Focus from the keyboard starts on the first slot;
// a click's focus does not, since the pointer chooses.
function useActiveSlot(n: number) {
  const [active, setActive] = useState<number | null>(null);
  const byPointer = useRef(false);
  const handlers = {
    onKeyDown: (event: KeyboardEvent<HTMLElement>) => {
      const to = moveActive(event.key, active, n);
      if (to !== undefined) {
        event.preventDefault();
        setActive(to);
      } else if (event.key === "Escape" && active !== null) {
        event.preventDefault();
        setActive(null);
      }
    },
    onPointerDown: () => {
      byPointer.current = true;
    },
    onFocus: () => {
      if (!byPointer.current && active === null && n > 0) {
        setActive(0);
      }
      byPointer.current = false;
    },
    onBlur: () => setActive(null),
    onPointerLeave: () => setActive(null),
  };
  return { active, setActive, handlers };
}

// The focusable plot. The active slot's tooltip shows over it and a live region says it, so a screen reader hears each
// point as the keys move through them.
function Plot({
  label,
  empty,
  series,
  points,
  words,
  scale,
  children,
}: Pick<ChartProps, "label" | "empty" | "series" | "points"> & {
  words: Words;
  scale: { step: number; top: number };
  children: (active: number | null) => ReactNode;
}) {
  const n = points.length;
  const { active, setActive, handlers } = useActiveSlot(n);
  const slot = plotWidth / Math.max(n, 1);
  const gridlines = Array.from({ length: ticks }, (_, at) => (at + 1) * scale.step);
  const shown = active === null ? undefined : points[active];
  return (
    <>
      {/* biome-ignore lint/a11y/useSemanticElements: a fieldset would draw a border and a legend for a group that is a picture. */}
      <div
        role="group"
        aria-label={label}
        // biome-ignore lint/a11y/noNoninteractiveTabindex: a keyboard reads the chart point by point from this group.
        tabIndex={0}
        className={s.plot}
        {...handlers}
      >
        <div className={s.yax} aria-hidden="true">
          {gridlines.map((value, at) => (
            <span key={value} style={{ "--at": ((at + 1) * 100) / ticks } as Vars}>
              {words.amount(value)}
            </span>
          ))}
        </div>
        <div className={s.canvas}>
          <svg viewBox={`0 0 ${plotWidth} ${plotHeight}`} preserveAspectRatio="none" aria-hidden="true">
            <g className={s.grid}>
              {gridlines.map((value) => {
                const y = plotHeight - (value / scale.top) * plotHeight;
                return <line key={value} x1="0" x2={plotWidth} y1={y} y2={y} />;
              })}
            </g>
            {n === 0 ? null : children(active)}
            {points.map((point, at) => (
              <rect
                key={point.at.getTime()}
                className={classes(s.hit, active === at && s.on)}
                x={slot * at}
                y="0"
                width={slot}
                height={plotHeight}
                onPointerEnter={() => setActive(at)}
              />
            ))}
          </svg>
          {n === 0 ? <p className={s.empty}>{empty}</p> : null}
          {shown !== undefined && active !== null ? (
            <Tip point={shown} x={((active + 0.5) / n) * 100} series={series} words={words} />
          ) : null}
        </div>
      </div>
      <div className={s.sr} role="status" aria-live="polite">
        {shown === undefined
          ? ""
          : `${words.slot(shown.at)}: ${series.map((one) => `${one.label} ${words.amount(clean(shown.values[one.key]))}`).join(", ")}`}
      </div>
    </>
  );
}

function Chart({ kind, ...props }: ChartProps & { kind: "bar" | "line" }) {
  const { series, points, unit, step, timeZone } = props;
  const words = useChartWords(unit, step, timeZone);
  const highest = Math.max(
    0,
    ...points.map((point) => {
      const values = series.map((one) => clean(point.values[one.key]));
      return kind === "bar" ? values.reduce((sum, value) => sum + value, 0) : Math.max(0, ...values);
    }),
  );
  const scale = scaleOf(highest, unit);
  const n = points.length;
  const labelEvery = Math.ceil(n / maxLabels);
  const yWidth = Math.max(...Array.from({ length: ticks }, (_, at) => words.amount((at + 1) * scale.step).length));
  return (
    <figure className={s.root} style={{ "--yw": yWidth } as Vars}>
      <Plot {...props} words={words} scale={scale}>
        {(active) =>
          kind === "bar" ? (
            <Bars series={series} points={points} top={scale.top} />
          ) : (
            <Lines series={series} points={points} top={scale.top} active={active} />
          )
        }
      </Plot>
      <div className={s.xax} aria-hidden="true">
        {points.map((point, at) =>
          at % labelEvery === 0 ? (
            <span key={point.at.getTime()} style={{ "--x": ((at + 0.5) / n) * 100 } as Vars}>
              {words.tick(point.at)}
            </span>
          ) : null,
        )}
      </div>
      <ul className={s.legend}>
        {series.map((one, order) => (
          <li key={one.key}>
            <i className={colour(order)} />
            {one.label}
          </li>
        ))}
      </ul>
      <ChartTable label={props.label} series={series} points={points} words={words} />
    </figure>
  );
}

// BarChart stacks its series in each slot.
export function BarChart(props: ChartProps) {
  return <Chart kind="bar" {...props} />;
}

// LineChart draws each series as its own line.
export function LineChart(props: ChartProps) {
  return <Chart kind="line" {...props} />;
}
