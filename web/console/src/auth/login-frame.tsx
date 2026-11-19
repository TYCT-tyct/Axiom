import LoginBrandName from "@axiom/boards/Login/regions/LoginBrandName";
import LoginChar from "@axiom/boards/Login/regions/LoginChar";
import LoginDecode from "@axiom/boards/Login/regions/LoginDecode";
import LoginLanguage from "@axiom/boards/Login/regions/LoginLanguage";
import LoginMark from "@axiom/boards/Login/regions/LoginMark";
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
import { loginPhoneWidth, useBoardMedia } from "../board-interaction";
import { siteQuery } from "../queries";
import type { Attempt } from "./attempt";
import { NoteAction } from "./login-parts";

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
// follow the console's accent, --accent, which brandStyle (@axiom/ui brand.ts) sets from the site's colour (the primary
// button stays neutral, R10). The controls' edges are the console's control edge, solid 1px, which holds 3:1 against
// what is around them (docs/frontend.md「对比度令牌」); the board's own edge is lighter, and only half a pixel on a
// dense screen.
const boardVars = [
  "--focus: var(--accent)",
  "--ring: var(--accent)",
  "--lineStrong1x: var(--ctl-line)",
  "--lineStrong2x: var(--ctl-line)",
  "--bw: 1px",
].join("; ");

// A page's handle on the stage over the sea: what it hears (the email's length, the digits typed, a refusal) reaches
// the engine, and the latest of each lasting kind is kept, so an engine that loads after the visitor has typed starts
// from what the form shows. The page makes it, so the forms it renders can tell it.
export type Stage = {
  readonly hear: (heard: Heard) => void;
  readonly running: RefObject<Login | null>;
  readonly said: RefObject<Map<Heard["type"], Heard>>;
};
export function useStage(): Stage {
  const running = useRef<Login | null>(null);
  const said = useRef(new Map<Heard["type"], Heard>());
  return useMemo(
    () => ({
      running,
      said,
      hear: (heard: Heard) => {
        // a refusal is a moment, not a state to start from
        if (heard.type !== "bad") said.current.set(heard.type, heard);
        running.current?.hear(heard);
      },
    }),
    [],
  );
}

// A site's name can be 100 characters (the board draws only short ones): the brand keeps clear of the language switch,
// which on a phone sits over the sea beside it, and the name wraps in the room left, anywhere if it must, whole on a
// wide window. A phone's sea is a band 208px high, with the pitch under the brand: three lines are what fits, so there a
// name longer than about 36 characters ends in an ellipsis rather than run into the pitch.
const brandStyle = "right: 128px";
const brandNameStyle = (phone: boolean) =>
  `min-width: 0; overflow-wrap: anywhere${phone ? "; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; overflow: hidden" : ""}`;

// The Login board's names for the two languages, as its switch shows them, as a screen reader says them, and the
// language each is written in.
const languages = {
  zh: { label: "中文", name: "中文", lang: "zh-CN" },
  en: { label: "EN", name: "English", lang: "en" },
} as const;

type FrameProps = {
  title: string;
  sub?: ReactNode;
  // the sub's own style, for a sentence that holds an address (wrapAnywhere)
  subStyle?: string;
  // which question the form asks: the form is kept while its words change language, and starts afresh for another
  step: string;
  shape: Shape;
  // the page's request (attempt.ts): while one is out the page takes nothing but its cancel
  attempt: Attempt;
  stage: Stage;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
};

// LoginFrame is a sign-in page as the Login board draws it (port design §9): the sea and the pitch on one side, one
// question on the other, in a form that submits through onSubmit. The sea, the stage that forms shapes on it and the
// title's decode on an arrival from the website come from @axiom/fx/login, loaded after the first content.
export function LoginFrame({ title, sub, subStyle, step, shape, attempt, stage, onSubmit, children }: FrameProps) {
  const { t, i18n } = useTranslation();
  const site = useQuery(siteQuery);
  const prefs = usePreferences();
  usePageTitle(title);
  useBrandStyle(site.data?.brand_color);
  const form = useRef<HTMLFormElement | null>(null);
  const cancel = useRef<HTMLDivElement | null>(null);
  useLock(form, cancel, attempt.kind !== null);
  useFocusOnStep(form, step);
  useReloadWhenRestored();
  const titleRef = useRef<HTMLElement | null>(null);
  const engine = useLoginEngine({
    stage,
    shape,
    dark: prefs.dark,
    brand: site.data?.brand_color,
    titleRef,
    title,
    step,
  });
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const name = site.data?.name ?? t("brand", { ns: "common" });
  const phone = useBoardMedia(loginPhoneWidth);
  return (
    <LoginShell
      t={(key) => (english ? ShellEN : ShellZH)[key]}
      values={{
        modeCls: prefs.dark ? "dark" : "light",
        lang: prefs.locale,
        noKern: english,
        vars: boardVars,
        seaCanvas: engine.seaCanvas,
        brandStyle,
      }}
      text={{ pitch0: t("auth.pitch.lead"), pitch1: t("auth.pitch.tail"), lede: t("auth.pitch.lede"), home: "/" }}
      brand={
        <>
          <BoardMark logoUrl={site.data?.logo_url} name={name} fallback={<LoginMark t={String} />} />
          <LoginBrandName t={String} text={{ name }} values={{ style: brandNameStyle(phone) }} />
        </>
      }
      languages={<Languages current={english ? "en" : "zh"} choose={prefs.other.choose} />}
    >
      <form ref={form} noValidate onSubmit={onSubmit} aria-busy={attempt.kind !== null}>
        <LoginStep key={step} t={String} values={{ stepCls: "in", shape }}>
          <FrameTitle title={title} titleRef={titleRef} />
          {sub ? (
            <LoginSub t={String} values={{ style: subStyle ?? "" }}>
              <span>{sub}</span>
            </LoginSub>
          ) : null}
          {children}
        </LoginStep>
      </form>
      <div ref={cancel}>
        {attempt.cancel ? <NoteAction label={t("cancel", { ns: "common" })} onPress={attempt.cancel} /> : null}
      </div>
      <LoginDecode t={String} values={{ canvas: engine.decodeCanvas }} />
    </LoginShell>
  );
}

// useLock makes the page inert while a request is out: the form, the sea with its links and the language switch take
// no press, no typing and no focus, and are out of the accessibility tree, so what is on screen is what was sent. The
// cancel stays outside them, and takes the focus the locked control lost; once free, the page gives it back. One rule
// for the page, so no control can be forgotten; a layout effect, so it is in place before the next event.
function useLock(form: RefObject<HTMLFormElement | null>, cancel: RefObject<HTMLDivElement | null>, locked: boolean) {
  useLayoutEffect(() => {
    const frame = form.current?.closest(".lg");
    if (!locked || !form.current || !frame) return;
    const active = document.activeElement;
    const had = active instanceof HTMLElement && active !== document.body ? active : null;
    const regions = [form.current, frame.querySelector(".lg-sea"), frame.querySelector(".lg-top")].filter(
      (region) => region instanceof HTMLElement,
    );
    for (const region of regions) region.inert = true;
    cancel.current?.querySelector<HTMLElement>("a")?.focus();
    return () => {
      for (const region of regions) region.inert = false;
      const lost = document.activeElement === document.body || cancel.current?.contains(document.activeElement);
      // the control that had it, or, where the request changed the step under it, the step's first control
      if (lost) (had?.isConnected ? had : firstControl(form.current))?.focus();
    };
  }, [form, cancel, locked]);
}

// Another step (the password, back to the address, a sign-in that is over) takes the focus to its first control, so
// a keyboard or a screen reader is where the question is; the first step keeps the page's own.
function useFocusOnStep(form: RefObject<HTMLFormElement | null>, step: string) {
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    firstControl(form.current)?.focus();
  }, [form, step]);
}

const firstControl = (form: HTMLFormElement | null) =>
  form?.querySelector<HTMLElement>("input:not([hidden]), button, a[href]");

// A page the browser shows again from its back-forward cache (back from a provider's page) is the page as the visitor
// left it, mid-request: it loads afresh instead, from the server's session, rather than guess what became of that.
function useReloadWhenRestored() {
  useEffect(() => {
    const restored = (event: PageTransitionEvent) => event.persisted && window.location.reload();
    window.addEventListener("pageshow", restored);
    return () => window.removeEventListener("pageshow", restored);
  }, []);
}

// useLoginEngine runs the sea, the stage and an arrival's decode on the frame once their code has loaded, and keeps
// them on the step's shape and the page's colours (the scheme, the site's brand). The decode ends when the title it
// plays over changes (another step, another language). It hands the running engine to the page's stage.
function useLoginEngine({
  stage,
  shape,
  dark,
  brand,
  titleRef,
  title,
  step,
}: {
  stage: Stage;
  shape: Shape;
  dark: boolean;
  brand: string | undefined;
  titleRef: RefObject<HTMLElement | null>;
  title: string;
  step: string;
}) {
  const handle = stage;
  const [arrived] = useState(claimArrival);
  const veil = useArrivalVeil(titleRef, arrived);
  const root = useRef<HTMLElement | null>(null);
  const decode = useRef<HTMLCanvasElement | null>(null);
  const latest = useRef({ shape, dark });
  latest.current = { shape, dark };
  const login = useEffectModule(root, loadLogin, (module, frame) => {
    const running = module.startLogin(frame, !latest.current.dark, latest.current.shape, arrived);
    if (veil.on.current && titleRef.current && decode.current) {
      veil.on.current = false;
      running.decode(titleRef.current, decode.current);
    }
    for (const heard of handle.said.current.values()) running.hear(heard);
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
  // brand is a dependency only: useBrandStyle, an effect before this one, has put its colours on the page
  useEffect(() => login.current?.recolour(!dark), [login, dark, brand]);
  // title and step are dependencies only: when either changes, the veil and the decode of the old title end
  useEffect(
    () => () => {
      veil.lift();
      login.current?.endDecode();
    },
    [login, veil, title, step],
  );
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
// is late; the colour goes through the CSSOM (CSP), and the decode gives each character's back. The veil lifts at once
// when motion is turned off, or when its title goes (lift): a decode that comes later does not play then.
function useArrivalVeil(titleRef: RefObject<HTMLElement | null>, arrived: boolean) {
  const on = useRef(false);
  const end = useRef(() => {});
  useLayoutEffect(() => {
    const chars = titleRef.current?.querySelectorAll<HTMLElement>(".ch") ?? [];
    if (!arrived || reducedMotion()) return;
    for (const c of chars) c.style.color = "transparent";
    on.current = true;
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    const lift = () => {
      on.current = false;
      window.clearTimeout(timer);
      motion.removeEventListener("change", lift);
      for (const c of chars) c.style.removeProperty("color");
    };
    const timer = window.setTimeout(lift, veilFor);
    motion.addEventListener("change", lift);
    end.current = lift;
    return lift;
  }, [arrived, titleRef]);
  return useMemo(() => ({ on, lift: () => on.current && end.current() }), []);
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

// The board's language switch: both languages, in the board's order, the page's own marked current. One part for both,
// so the one pressed stays the same element, with the focus, as it becomes current.
function Languages({ current, choose }: { current: "zh" | "en"; choose: () => void }) {
  return (["zh", "en"] as const).map((language) => (
    <LoginLanguage
      key={language}
      t={String}
      text={{ label: languages[language].label }}
      values={{
        current: language === current || undefined,
        name: languages[language].name,
        lang: languages[language].lang,
        onPress: (event) => {
          event.preventDefault();
          if (language !== current) choose();
        },
      }}
    />
  ));
}
