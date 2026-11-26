import { classes } from "../classes";
import s from "./delta.module.css";

// Delta is how far a number moved (the admin's adDelta): an arrow up or down and the size of the move with its unit
// ("3.9%", " 秒"). tone says whether the move is good or bad for the business (revenue up is good, failures up are
// bad); a move that is neither keeps the text colour. The arrow is drawn, so the sign is also written for a screen
// reader as the arrow's name, never carried by colour or shape alone.
export function Delta({ value, unit = "%", tone }: { value: number; unit?: string; tone?: "good" | "bad" }) {
  const up = value >= 0;
  return (
    <em className={classes(s.root, tone && s[tone])}>
      <svg viewBox="0 0 12 12" role="img" aria-label={up ? "+" : "−"}>
        <path d={up ? "M6 9.5v-7M3 5.5l3-3 3 3" : "M6 2.5v7M3 6.5l3 3 3-3"} />
      </svg>
      {Math.abs(value)}
      {unit}
    </em>
  );
}
