import LoginChar from "@axiom/boards/Login/regions/LoginChar";
import LoginDecode from "@axiom/boards/Login/regions/LoginDecode";
import LoginLanguage from "@axiom/boards/Login/regions/LoginLanguage";
import LoginMark from "@axiom/boards/Login/regions/LoginMark";
import LoginOtherLanguage from "@axiom/boards/Login/regions/LoginOtherLanguage";
import LoginShell from "@axiom/boards/Login/regions/LoginShell";
import ShellEN from "@axiom/boards/Login/regions/LoginShell.en";
import ShellZH from "@axiom/boards/Login/regions/LoginShell.zh";
import LoginStep from "@axiom/boards/Login/regions/LoginStep";
import LoginSub from "@axiom/boards/Login/regions/LoginSub";
import LoginTitle from "@axiom/boards/Login/regions/LoginTitle";
import { reducedMotion, takeArrival, useEffectModule } from "@axiom/fx";
import type { Heard, Login, Shape } from "@axiom/fx/login";
import { BoardMark, useBrandStyle, usePageTitle, usePreferences } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import {
  type FormEvent,
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
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

// The console's tokens on the board's own controls. The focus ring and the accent the stage and the decode draw in
// follow the console's accent, which brandStyle derives from the site's colour (the primary button stays neutral,
// R10). The controls' edges are the console's control edge, solid 1px, which holds 3:1 against what is around them
// (docs/frontend.md「对比度令牌」); the board's own edge is lighter, and only half a pixel on a dense screen.
const boardVars = [
  "--focus: var(--accentT)",
  "--ring: var(--accentT)",
  "--lineStrong1x: var(--ctl-line)",
  "--lineStrong2x: var(--ctl-line)",
  "--bw: 1px",
].join("; ");

// A page's handle on the stage over the sea: what it hears (the email's length, the digits typed, a refusal) reaches
// the engine once it has loaded, and nothing before. The page makes it, so the forms it renders can tell it.
export type Stage = { readonly hear: (heard: Heard) => void; readonly running: RefObject<Login | null> };
export function useStage(): Stage {
  const running = useRef<Login | null>(null);
  return useMemo(() => ({ running, hear: (heard: Heard) => running.current?.hear(heard) }), []);
}

// The Login board's names for the two languages, as its switch shows them.
const languageLabels = { zh: "中文", en: "EN" } as const;

type FrameProps = {
  title: string;
  sub?: ReactNode;
  // which question the form asks: the form is kept while its words change language, and starts afresh for another
  step: string;
  shape: Shape;
  stage?: Stage;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
};

// LoginFrame is a sign-in page as the Login board draws it (port design §9): the sea and the pitch on one side, one
// question on the other, in a form that submits through onSubmit. The sea, the stage that forms shapes on it and the
// title's decode on an arrival from the website come from @axiom/fx/login, loaded after the first content.
export function LoginFrame({ title, sub, step, shape, stage, onSubmit, children }: FrameProps) {
  const { t, i18n } = useTranslation();
  const site = useQuery(siteQuery);
  const prefs = usePreferences();
  usePageTitle(title);
  useBrandStyle(site.data?.brand_color);
  const titleRef = useRef<HTMLElement | null>(null);
  const engine = useLoginEngine({ stage, shape, dark: prefs.dark, titleRef });
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const name = site.data?.name ?? t("brand", { ns: "common" });
  return (
    <LoginShell
      t={(key) => (english ? ShellEN : ShellZH)[key]}
      values={{
        modeCls: prefs.dark ? "dark" : "light",
        lang: prefs.locale,
        noKern: english,
        vars: boardVars,
        seaCanvas: engine.seaCanvas,
      }}
      text={{ pitch0: t("auth.pitch.lead"), pitch1: t("auth.pitch.tail"), home: "/" }}
      brand={
        <>
          <BoardMark logoUrl={site.data?.logo_url} name={name} fallback={<LoginMark t={String} />} />
          <span>{name}</span>
        </>
      }
      languages={<Languages current={english ? "en" : "zh"} choose={prefs.other.choose} />}
    >
      <form noValidate onSubmit={onSubmit}>
        <LoginStep key={step} t={String} values={{ stepCls: "in", shape }}>
          <FrameTitle title={title} titleRef={titleRef} />
          {sub ? <LoginSub t={String}>{sub}</LoginSub> : null}
          {children}
        </LoginStep>
      </form>
      <LoginDecode t={String} values={{ canvas: engine.decodeCanvas }} />
    </LoginShell>
  );
}

// useLoginEngine runs the sea, the stage and an arrival's decode on the frame once their code has loaded, keeps them
// on the step's shape and the scheme, and hands the running engine to the page's stage.
function useLoginEngine({
  stage,
  shape,
  dark,
  titleRef,
}: {
  stage?: Stage;
  shape: Shape;
  dark: boolean;
  titleRef: RefObject<HTMLElement | null>;
}) {
  const own = useStage();
  const handle = stage ?? own;
  const [arrived] = useState(claimArrival);
  const veiled = useArrivalVeil(titleRef, arrived);
  const root = useRef<HTMLElement | null>(null);
  const decode = useRef<HTMLCanvasElement | null>(null);
  const latest = useRef({ shape, dark });
  latest.current = { shape, dark };
  const login = useEffectModule(root, loadLogin, (module, frame) => {
    const running = module.startLogin(frame, !latest.current.dark, latest.current.shape, arrived);
    if (veiled.current && titleRef.current && decode.current) {
      veiled.current = false;
      running.decode(titleRef.current, decode.current);
    }
    handle.running.current = running;
    return running;
  });
  useEffect(
    () => () => {
      handle.running.current = null;
    },
    [handle],
  );
  useEffect(() => login.current?.show(shape), [login, shape]);
  useEffect(() => login.current?.sea.engine.setLight(!dark), [login, dark]);
  return {
    seaCanvas: (canvas: Element | null) => {
      root.current = canvas?.closest<HTMLElement>(".lg") ?? null;
    },
    decodeCanvas: (element: Element | null) => {
      decode.current = element as HTMLCanvasElement | null;
    },
  };
}

// An arrival's title is hidden from its first frame until the decode resolves it, or for veilFor if the decode's code
// is late; the colour goes through the CSSOM (CSP), and the decode gives each character's back.
function useArrivalVeil(titleRef: RefObject<HTMLElement | null>, arrived: boolean) {
  const veiled = useRef(false);
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
  }, [arrived, titleRef]);
  return veiled;
}

function FrameTitle({ title, titleRef }: { title: string; titleRef: RefObject<HTMLElement | null> }) {
  return (
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
        <LoginChar key={`${i}${char}`} t={String} text={{ char: char === " " ? " " : char }} />
      ))}
    </LoginTitle>
  );
}

// The board's language switch: the page's own language marked current, the other one a press away, in the board's
// order whichever is current.
function Languages({ current, choose }: { current: "zh" | "en"; choose: () => void }) {
  return (["zh", "en"] as const).map((language) =>
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
            choose();
          },
        }}
      />
    ),
  );
}
