import { type CSSProperties, createContext, type ReactNode, useContext, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { settled } from "../motion";
import { Glyph } from "./glyph";
import { SubmitFailed } from "./submit-failed";
import s from "./toast.module.css";

type Action = { label: string; run: () => void };
type Shown = { id: number; text: string; bad: boolean; action?: Action };

// Say shows one line of feedback after an action, replacing the one before. bad is a failure, read out at once; an
// action is the one thing to do about it, such as undo.
export type Say = (text: string, options?: { bad?: boolean; action?: Action }) => void;

const SayContext = createContext<Say | null>(null);

// ToastProvider holds the one toast an app shows at a time; a page says something with useToast.
export function ToastProvider({ children }: { children: ReactNode }) {
  const [shown, setShown] = useState<Shown | null>(null);
  const count = useRef(0);
  const say = useRef<Say>((text, { bad = false, action } = {}) => {
    count.current += 1;
    setShown({ id: count.current, text, bad, action });
  }).current;
  return (
    <SayContext value={say}>
      {children}
      {shown ? (
        <Toast key={shown.id} {...shown} onGone={() => setShown((now) => (now?.id === shown.id ? null : now))} />
      ) : null}
    </SayContext>
  );
}

export function useToast(): Say {
  const say = useContext(SayContext);
  if (!say) {
    throw new Error("useToast is used outside a ToastProvider");
  }
  return say;
}

// A toast stays 2.6 seconds, or 5.2 with an action to take; pointing at it or focusing it holds it, and it goes 1.6
// seconds after the pointer or the focus leaves (the board's timing).
const stay = { plain: 2600, action: 5200, after: 1600 };

function Toast({ text, bad, action, onGone }: Omit<Shown, "id"> & { onGone: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [leaving, setLeaving] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const ms = action ? stay.action : stay.plain;
  const hold = () => clearTimeout(timer.current);
  const arm = (delay: number) => {
    hold();
    timer.current = setTimeout(() => setLeaving(true), delay);
  };
  useEffect(() => {
    arm(ms);
    return hold;
  }, []);
  useEffect(() => {
    if (leaving && ref.current) {
      settled(ref.current).then(onGone);
    }
  }, [leaving]);
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the pointer only holds the toast; the action is its button.
    <div
      ref={ref}
      role={bad ? "alert" : "status"}
      className={classes(s.root, bad && s.bad, leaving && s.leaving)}
      style={{ "--ms": `${ms}ms` } as CSSProperties}
      onMouseEnter={hold}
      onMouseLeave={() => arm(stay.after)}
      onFocus={hold}
      onBlur={() => arm(stay.after)}
    >
      <span className={s.icon}>
        <Glyph name={bad ? "warn" : "check"} />
      </span>
      <span className={s.text}>{text}</span>
      {action ? (
        <>
          <button
            type="button"
            className={s.action}
            onClick={() => {
              hold();
              setLeaving(true);
              action.run();
            }}
          >
            {action.label}
          </button>
          <i aria-hidden="true" className={s.bar} />
        </>
      ) : null}
    </div>
  );
}

// useUndo offers to take back an action that has a reverse (disabling a key and enabling it again, docs/frontend.md
// 「写入策略」): a toast with an undo button that sends the reverse request. It is never a client-side rollback, and a
// deletion is confirmed instead. failed is the SubmitFailed to show where the action was taken when the reverse
// request fails, null otherwise.
export function useUndo() {
  const { t } = useTranslation("common");
  const say = useToast();
  const [failure, setFailure] = useState<unknown>(null);
  const offer = (text: string, reverse: () => Promise<unknown>) => {
    setFailure(null);
    say(text, {
      action: {
        label: t("toast.undo"),
        run: () =>
          reverse().then(
            () => say(t("toast.undone")),
            (error: unknown) => setFailure(() => error),
          ),
      },
    });
  };
  return { offer, failed: failure ? <SubmitFailed error={failure} next={t("toast.undoFailed")} /> : null };
}
