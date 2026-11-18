import { useErrors, useSignOut } from "@axiom/app";
import LoginAction from "@axiom/boards/Login/regions/LoginAction";
import LoginChar from "@axiom/boards/Login/regions/LoginChar";
import LoginDecode from "@axiom/boards/Login/regions/LoginDecode";
import LoginError from "@axiom/boards/Login/regions/LoginError";
import LoginLanguage from "@axiom/boards/Login/regions/LoginLanguage";
import LoginMark from "@axiom/boards/Login/regions/LoginMark";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginOtherLanguage from "@axiom/boards/Login/regions/LoginOtherLanguage";
import LoginShell from "@axiom/boards/Login/regions/LoginShell";
import ShellEN from "@axiom/boards/Login/regions/LoginShell.en";
import ShellZH from "@axiom/boards/Login/regions/LoginShell.zh";
import LoginStep from "@axiom/boards/Login/regions/LoginStep";
import LoginSub from "@axiom/boards/Login/regions/LoginSub";
import LoginTitle from "@axiom/boards/Login/regions/LoginTitle";
import { reducedMotion, takeArrival, useEffectModule } from "@axiom/fx";
import type { Heard, Shape } from "@axiom/fx/login";
import { BoardMark, useBrandStyle, usePageTitle, usePreferences } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
  use,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { pressKey } from "../board";
import { siteQuery } from "../queries";

const loadLogin = () => import("@axiom/fx/login");

// The departure's marker is taken by the first sign-in page this document shows, the page the departure opened: the
// next step (two-step verification, consent) is not an arrival.
let unclaimed = true;
function claimArrival() {
  if (!unclaimed) return false;
  unclaimed = false;
  return takeArrival();
}

// How long an arrival's title waits, hidden, for the decode's code before it is shown as it is.
const veilFor = 1500;

const StageContext = createContext<(heard: Heard) => void>(() => {});
// useHear tells the stage on the sea what the visitor is doing (the email's length, the digits typed, a refusal).
export const useHear = () => use(StageContext);

// The Login board's names for the two languages, as its switch shows them.
const languageLabels = { zh: "中文", en: "EN" } as const;

// LoginFrame is a sign-in page as the Login board draws it (port design §9): the sea and the pitch on one side, one
// question on the other, in a form that submits through onSubmit. The sea, the stage that forms shape on it and the
// title's decode on an arrival from the website come from @axiom/fx/login, loaded after the first content.
export function LoginFrame({
  title,
  sub,
  shape,
  onSubmit,
  children,
}: {
  title: string;
  sub?: ReactNode;
  shape: Shape;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
}) {
  const { t, i18n } = useTranslation();
  const site = useQuery(siteQuery);
  const prefs = usePreferences();
  usePageTitle(title);
  useBrandStyle(site.data?.brand_color);
  const [arrived] = useState(claimArrival);
  const root = useRef<HTMLElement | null>(null);
  const titleRef = useRef<HTMLElement | null>(null);
  const decodeCanvas = useRef<HTMLCanvasElement | null>(null);
  const veiled = useRef(false);
  const latest = useRef({ shape, dark: prefs.dark });
  latest.current = { shape, dark: prefs.dark };

  // An arrival's title is hidden from its first frame until the decode resolves it, or for veilFor if the decode's
  // code is late; the colour goes through the CSSOM (CSP), and the decode gives each character's back.
  useLayoutEffect(() => {
    const chars = titleRef.current?.querySelectorAll<HTMLElement>(".ch") ?? [];
    if (!arrived || reducedMotion()) return;
    for (const c of chars) c.style.color = "transparent";
    veiled.current = true;
    const timer = window.setTimeout(() => {
      veiled.current = false;
      for (const c of chars) c.style.removeProperty("color");
    }, veilFor);
    return () => window.clearTimeout(timer);
  }, [arrived]);

  const login = useEffectModule(root, loadLogin, (module, frame) => {
    const running = module.startLogin(frame, !latest.current.dark, latest.current.shape, arrived);
    if (veiled.current && titleRef.current && decodeCanvas.current) {
      veiled.current = false;
      running.decode(titleRef.current, decodeCanvas.current);
    }
    return running;
  });
  useEffect(() => login.current?.show(shape), [login, shape]);
  useEffect(() => login.current?.sea.engine.setLight(!prefs.dark), [login, prefs.dark]);
  const hear = useCallback((heard: Heard) => login.current?.hear(heard), [login]);

  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const current = english ? "en" : "zh";
  const name = site.data?.name ?? t("brand", { ns: "common" });
  const languages = (["zh", "en"] as const).map((language) =>
    language === current ? (
      <LoginLanguage key={language} t={String} text={{ label: languageLabels[language] }} />
    ) : (
      <LoginOtherLanguage
        key={language}
        t={String}
        text={{ label: languageLabels[language] }}
        values={{
          onPress: (event) => {
            event.preventDefault();
            prefs.other.choose();
          },
        }}
      />
    ),
  );
  return (
    <StageContext value={hear}>
      <LoginShell
        t={(key) => (english ? ShellEN : ShellZH)[key]}
        values={{
          modeCls: prefs.dark ? "dark" : "light",
          lang: prefs.locale,
          noKern: english,
          seaCanvas: (canvas) => {
            root.current = canvas?.closest<HTMLElement>(".lg") ?? null;
          },
        }}
        text={{ pitch0: t("auth.pitch.lead"), pitch1: t("auth.pitch.tail"), home: "/" }}
        brand={
          <>
            <BoardMark logoUrl={site.data?.logo_url} name={name} fallback={<LoginMark t={String} />} />
            <span>{name}</span>
          </>
        }
        languages={languages}
      >
        <form noValidate onSubmit={onSubmit}>
          <LoginStep key={title} t={String} values={{ stepCls: "in", shape }}>
            <LoginTitle
              t={String}
              text={{ label: title }}
              values={{
                title: (element) => {
                  titleRef.current = element as HTMLElement | null;
                },
              }}
            >
              {[...title].map((char, i) => (
                <LoginChar key={i} t={String} text={{ char: char === " " ? " " : char }} />
              ))}
            </LoginTitle>
            {sub ? <LoginSub t={String}>{sub}</LoginSub> : null}
            {children}
          </LoginStep>
        </form>
        <LoginDecode
          t={String}
          values={{
            canvas: (element) => {
              decodeCanvas.current = element as HTMLCanvasElement | null;
            },
          }}
        />
      </LoginShell>
    </StageContext>
  );
}

// FormProblem is a step's error line, announced as it appears; id lets the field it is about point at it.
export function FormProblem({ id, message }: { id?: string; message: ReactNode }) {
  return message ? <LoginError t={String} text={{ message: String(message) }} values={{ id: id ?? "" }} /> : null;
}

// NoteAction is a note under a step whose link does something on this page (go back a step, sign out, try again).
export function NoteAction({ label, onPress, before }: { label: string; onPress: () => void; before?: ReactNode }) {
  return (
    <LoginNote t={String}>
      {before}
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
