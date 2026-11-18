import { ApiError } from "@axiom/api";
import { useErrors, useSignOut } from "@axiom/app";
import LoginAction from "@axiom/boards/Login/regions/LoginAction";
import LoginError from "@axiom/boards/Login/regions/LoginError";
import LoginLink from "@axiom/boards/Login/regions/LoginLink";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import { Link } from "@tanstack/react-router";
import { type AnchorHTMLAttributes, Fragment, type KeyboardEvent, type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { pressKey } from "../board";

// The parts the sign-in pages' steps share, made of the Login board's parts.

// FormProblem is a step's error line, announced as it appears; id lets the field it is about point at it.
export function FormProblem({ id, message }: { id?: string; message: ReactNode }) {
  return message ? <LoginError t={String} text={{ message: String(message) }} values={{ id: id ?? "" }} /> : null;
}

// NoteAction is a note under a step whose link does something on this page (go back a step, sign out, try again).
export function NoteAction({ label, onPress }: { label: string; onPress: () => void }) {
  return (
    <LoginNote t={String}>
      <LoginAction
        t={String}
        label={label}
        values={{
          onPress: (event) => {
            event.preventDefault();
            onPress();
          },
          onKeyDown: (event) => pressKey(event as KeyboardEvent<Element>),
        }}
      />
    </LoginNote>
  );
}

// The router's link where the board draws a note's <a>: a sign-in page of this app, and the way back from it.
export function RouteLink({ href = "/", ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) {
  const [to, query = ""] = href.split("?");
  const next = new URLSearchParams(query).get("next") ?? undefined;
  // `to` is known only at run time; every sign-in page it names takes the same search (router.tsx guestSearch)
  return <Link to={to} search={{ next, invite: undefined } as never} {...props} />;
}

// withNext is a sign-in page's address with the way back the visitor came with.
export const withNext = (to: string, next?: string) => (next ? `${to}?next=${encodeURIComponent(next)}` : to);

// NoteLink is a note under a step whose link goes to another sign-in page, carrying where the visitor was going.
export function NoteLink({ label, to, next }: { label: string; to: string; next?: string }) {
  return (
    <LoginNote t={String}>
      <LoginLink t={String} link={RouteLink} label={label} values={{ to: withNext(to, next) }} />
    </LoginNote>
  );
}

// OtherAccount ends a session that stopped halfway (a pending second step, terms not yet accepted), to start again as
// someone else; a sign-out that fails says the visitor is still signed in.
export function OtherAccount() {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const signOut = useSignOut();
  return (
    <>
      <NoteAction label={t("auth.mfa.otherAccount")} onPress={() => !signOut.isPending && signOut.mutate()} />
      <FormProblem message={signOut.error ? t("signOutFailed", { reason: errors.message(signOut.error) }) : ""} />
    </>
  );
}

// WithEmail puts an address into a sentence where it may break anywhere: an address can be one unbroken run of 64
// letters, which would otherwise widen a phone's form. The break points are <wbr>, which copying leaves out.
const slot = "\u0000";
export function WithEmail({ sentence, email }: { sentence: (email: string) => string; email: string }) {
  const [before = "", after = ""] = sentence(slot).split(slot);
  return (
    <>
      {before}
      {[...email].map((char, i) => (
        <Fragment key={`${i}${char}`}>
          {char}
          <wbr />
        </Fragment>
      ))}
      {after}
    </>
  );
}

// useCooldown says whether a step's submit must wait: a 429 names how long (Retry-After), and sending again sooner is
// refused the same way, so the button stays disabled until then.
export function useCooldown(error: unknown) {
  const [waiting, setWaiting] = useState(false);
  useEffect(() => {
    if (!(error instanceof ApiError && error.status === 429)) return;
    setWaiting(true);
    const timer = window.setTimeout(() => setWaiting(false), (error.retryAfter ?? 60) * 1000);
    return () => {
      window.clearTimeout(timer);
      setWaiting(false);
    };
  }, [error]);
  return waiting;
}
