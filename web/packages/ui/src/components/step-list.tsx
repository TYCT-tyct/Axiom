import type { Key, ReactNode } from "react";
import { classes } from "../classes";
import { Icon } from "./icon";
import s from "./step-list.module.css";

export type ListedStep = { key: Key; title: string; detail?: ReactNode; done: boolean; action?: ReactNode };

// StepList is a checklist of the steps a thing takes before it is ready (the admin's .adsteps, a channel's 接入检查):
// each step numbered in a ring, a tick once done, the first one not done ringed darker as the one to do now, with its
// action beside it. A screen reader hears the steps as an ordered list, and the current one as the current step.
export function StepList({ label, steps }: { label: string; steps: readonly ListedStep[] }) {
  const next = steps.findIndex((step) => !step.done);
  return (
    <ol aria-label={label} className={s.root}>
      {steps.map((step, at) => (
        <li
          key={step.key}
          className={classes(step.done && s.done, at === next && s.current)}
          aria-current={at === next ? "step" : undefined}
        >
          <span className={s.mark}>{step.done ? <Icon name="check" width="14" height="14" /> : at + 1}</span>
          <div className={s.text}>
            <b>{step.title}</b>
            {step.detail ? <span>{step.detail}</span> : null}
          </div>
          {at === next ? step.action : null}
        </li>
      ))}
    </ol>
  );
}
