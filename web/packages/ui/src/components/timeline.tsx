import type { Key, ReactNode } from "react";
import { classes } from "../classes";
import s from "./timeline.module.css";

// done went through, current is where it is now, failed stopped it, pending is not reached.
export type TimelineStep = {
  key: Key;
  title: ReactNode;
  detail?: ReactNode;
  state?: "done" | "current" | "failed" | "pending";
};

// Timeline is the steps a thing went through (the admin's adTl: an order, a request): a dot per step on one hairline
// that runs from each dot to the next. A step that went through is the data colour, the current one a ring, a failed
// one red with its title in the state's text colour, one not reached grey. The detail says what happened in words.
export function Timeline({ label, steps }: { label: string; steps: readonly TimelineStep[] }) {
  return (
    <ol aria-label={label} className={s.root}>
      {steps.map((step) => (
        <li
          key={step.key}
          className={classes(s.step, s[step.state ?? "done"])}
          aria-current={step.state === "current" ? "step" : undefined}
        >
          <i className={s.dot} />
          <div className={s.text}>
            <b className={s.title}>{step.title}</b>
            {step.detail ? <span className={s.detail}>{step.detail}</span> : null}
          </div>
        </li>
      ))}
    </ol>
  );
}
