import { useCallback, useEffect, useRef, useState } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./toast.module.css";

type ToastKind = "ok" | "bad" | "info";
type ToastAction = { label: string; onClick: () => void };

const exitMs = 260;
const resumeMs = 1600;

function ToastIcon({ kind }: { kind: ToastKind }) {
  if (kind === "ok") {
    return (
      <svg viewBox="0 0 20 20" aria-hidden="true">
        <path d="m4.5 10.5 3.5 3.5 7.5-8" />
      </svg>
    );
  }
  return (
    <svg viewBox="0 0 20 20" aria-hidden="true">
      <circle cx="10" cy="10" r="6.75" />
      <path d={kind === "bad" ? "M10 6.75v4M10 13.25v.01" : "M10 9.25v4M10 6.75v.01"} />
    </svg>
  );
}

function useToastLifecycle(ms: number, onDismiss: () => void) {
  const [phase, setPhase] = useState<"in" | "on" | "out">("in");
  const hideTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const exitTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const leaving = useRef(false);
  const dismissRef = useRef(onDismiss);
  dismissRef.current = onDismiss;

  const leave = useCallback(() => {
    if (leaving.current) return;
    leaving.current = true;
    clearTimeout(hideTimer.current);
    setPhase("out");
    exitTimer.current = setTimeout(() => dismissRef.current(), exitMs);
  }, []);

  const pause = useCallback(() => clearTimeout(hideTimer.current), []);
  const arm = useCallback(
    (delay: number) => {
      clearTimeout(hideTimer.current);
      if (!leaving.current) hideTimer.current = setTimeout(leave, delay);
    },
    [leave],
  );

  useEffect(() => {
    const frame = requestAnimationFrame(() => setPhase("on"));
    arm(ms);
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !document.querySelector("dialog[open]")) leave();
    };
    document.addEventListener("keydown", onKey);
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(hideTimer.current);
      clearTimeout(exitTimer.current);
      document.removeEventListener("keydown", onKey);
    };
  }, [arm, leave, ms]);

  return { phase, leave, pause, resume: () => arm(resumeMs) };
}

// Toast is the board's one-line feedback bar after an action (fxSay in AxApp.logic.html, .fxt in AxApp/regions.css),
// with an optional single action such as undo or retry and a countdown bar that pauses on hover or focus.
export function Toast({
  message,
  kind = "ok",
  action,
  duration,
  onDismiss,
}: {
  message: string;
  kind?: ToastKind;
  action?: ToastAction;
  duration?: number;
  onDismiss: () => void;
}) {
  const ms = duration ?? (action ? 5200 : 2600);
  const { phase, leave, pause, resume } = useToastLifecycle(ms, onDismiss);

  return (
    <div className={classes(board.board, board.ax, board.ov, s.host)}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: pointer hover pauses auto-dismiss on a live status bar. */}
      <div
        role={kind === "bad" ? "alert" : "status"}
        className={classes(
          board.fxt,
          kind !== "info" && board[kind],
          phase === "on" && board.on,
          phase === "out" && board.out,
          s.root,
        )}
        onMouseEnter={action ? pause : undefined}
        onMouseLeave={action ? resume : undefined}
        onFocus={action ? pause : undefined}
        onBlur={action ? resume : undefined}
      >
        <ToastIcon kind={kind} />
        <span>{message}</span>
        {action ? (
          <>
            <button
              type="button"
              className={s.action}
              onClick={() => {
                leave();
                action.onClick();
              }}
            >
              {action.label}
            </button>
            <i aria-hidden="true" className={s.bar} style={{ "--ms": `${ms}ms` } as Record<`--${string}`, string>} />
          </>
        ) : null}
      </div>
    </div>
  );
}
