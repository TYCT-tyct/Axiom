import { useCachedSession, useReadSession } from "@axiom/app";
import type { Shape } from "@axiom/fx/login";
import { useSearch } from "@tanstack/react-router";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Attempt } from "./attempt";
import { LoginFrame, type Stage } from "./login-frame";
import { NoteAction, RouteNote, SignOutNote, wrapAnywhere } from "./login-parts";

// The way to sign in from a page without a gate (a reset, an address verified). Such a page works whoever this browser
// is signed in as, and the sign-in page opens the console of whoever is signed in. So the way to sign in is offered
// only once the browser is known to be signed out (useBrowserSession); otherwise the step says what the browser holds,
// truthfully (useSaid), and offers what fits it: signed in, to sign out first; a sign-in waiting for its second step,
// to finish it or end it; unread, to read again. Not knowing is never signed out.
export function WayOn({ browser, signIn, attempt }: { browser: BrowserSession; signIn: string; attempt: Attempt }) {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  if (browser.state === "out") return <RouteNote to="/sign-in" label={signIn} next={next} />;
  if (browser.state === "in") return <SignOutNote label={t("auth.way.signOut")} attempt={attempt} />;
  if (browser.state === "failed") return <NoteAction label={t("auth.way.check")} onPress={browser.check} />;
  if (browser.state === "reading") return null;
  return (
    <>
      <RouteNote to="/mfa" label={t("auth.way.goOn")} next={next} />
      <SignOutNote label={t("auth.way.endPending")} attempt={attempt} />
    </>
  );
}

// AnswerStep is a request's answer that leads on to sign in (the mail is on its way, the password is reset, the address
// is verified): its title takes the focus, its line says what the browser holds, and its way to sign in is WayOn's.
// children are the step's other ways (ask again, register again).
type AnswerProps = {
  title: string;
  sub: string;
  step: string;
  shape: Shape;
  signIn: string;
  attempt: Attempt;
  stage: Stage;
  children?: ReactNode;
};

export function AnswerStep({ title, sub, step, shape, signIn, attempt, stage, children }: AnswerProps) {
  const browser = useBrowserSession();
  const said = useSaid(browser);
  return (
    <LoginFrame
      title={title}
      sub={said(sub)}
      subStyle={wrapAnywhere}
      step={step}
      shape={shape}
      attempt={attempt}
      stage={stage}
      answer
      onSubmit={(event) => event.preventDefault()}
    >
      <WayOn browser={browser} signIn={signIn} attempt={attempt} />
      {children}
    </LoginFrame>
  );
}

// AfterStep is the answer to a mailed link's step (a reset, a verification): done, or why not, and the way on.
export function AfterStep({ after, ...answer }: { after: string } & Omit<AnswerProps, "step" | "shape">) {
  return <AnswerStep step={after} shape={after === "done" ? "enter" : "start"} {...answer} />;
}

export type BrowserSession = ReturnType<typeof useBrowserSession>;

// useSaid puts what the browser holds after a step's own sentence, in the step's line under its title, announced as it
// changes: that a read is out, whom the browser is signed in as, or that it could not be told.
export function useSaid(browser: BrowserSession) {
  const { t, i18n } = useTranslation();
  const said = {
    reading: t("auth.way.checking"),
    failed: t("auth.way.unsure"),
    in: t("auth.way.signedIn", { email: browser.email }),
    pending: t("auth.way.pending", { email: browser.email }),
    out: "",
  }[browser.state];
  const gap = (i18n.resolvedLanguage ?? i18n.language).startsWith("en") ? " " : "";
  return (own: string) => <span role="status">{[own, said].filter(Boolean).join(gap)}</span>;
}

// useBrowserSession is who this browser is signed in as, read afresh when the step opens: the same read a sign-in
// confirms its session with (useReadSession), after which the cache follows the session (a sign-out here empties it).
// Only the latest read decides.
export function useBrowserSession() {
  const read = useReadSession();
  const session = useCachedSession();
  const [known, setKnown] = useState<"reading" | "failed" | "read">("reading");
  const latest = useRef(0);
  const check = useCallback(() => {
    const n = ++latest.current;
    setKnown("reading");
    read().then(
      () => latest.current === n && setKnown("read"),
      () => latest.current === n && setKnown("failed"),
    );
  }, [read]);
  useEffect(check, [check]);
  const held = !session ? "out" : session.level === "pending_mfa" ? "pending" : "in";
  const state = known === "read" ? held : known;
  return { state, email: session?.user.email ?? "", check } as const;
}
