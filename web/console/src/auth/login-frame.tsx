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
import { takeArrival, useEffectModule } from "@axiom/fx";
import type { Heard, Login, Shape } from "@axiom/fx/login";
import { BoardMark, useBrandStyle, usePageTitle, usePreferences } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Outlet } from "@tanstack/react-router";
import {
  createContext,
  type FormEvent,
  type ReactNode,
  type RefObject,
  use,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { loginPhoneWidth, useBoardMedia } from "../board-interaction";
import { legalQuery, siteQuery } from "../queries";
import type { Attempt } from "./attempt";
import { NoteAction } from "./login-parts";
import { useReturnHome } from "./return-home";

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

// The console's tokens on the board's own controls. The focus ring and the accent the solid and the decode draw in
// follow the console's accent, --accent, which brandStyle (@axiom/ui brand.ts) sets from the site's colour (the primary
// button stays neutral, R10). The controls' edges are the console's control edge, solid 1px, which holds 3:1 against
// what is around them (docs/frontend.md「对比度令牌」); the board's own edge is lighter, and only half a pixel on a
// dense screen.
const boardVars = [
  "--focus: var(--accentT)",
  "--ring: var(--accentT)",
  "--lineStrong1x: var(--ctl-line)",
  "--lineStrong2x: var(--ctl-line)",
  "--bw: 1px",
].join("; ");

// A page's handle on the solid over the sea: what it hears (whether the address looks whole, the digits typed, a
// refusal) reaches the engine, and the latest of each lasting kind is kept, so an engine that loads after the visitor
// has typed starts from what the form shows. The page makes it, so the forms it renders can tell it.
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

// The Login board's names for the two languages, as its switch shows them, as a screen reader says them (beginning with
// the shown label, which WCAG 2.5.3 asks of a control's name), and the language each is written in.
const languages = {
  zh: { label: "中文", name: "中文", lang: "zh-CN" },
  en: { label: "EN", name: "EN, English", lang: "en" },
} as const;

type Frame = {
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
  // the step is a request's answer, in place of the form that sent it: its title takes the focus (useFocusAnswer)
  answer?: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
};

// What the page on show tells the frame it stands in: its title, its question and the solid for it, the stage its forms
// tell, and whether a request of it is out.
type Standing = {
  title: string;
  step: string;
  shape: Shape;
  stage: Stage;
  locked: boolean;
  titleRef: RefObject<HTMLElement | null>;
};
type Room = { slot: HTMLElement | null; stand: (standing: Standing) => void };
const RoomContext = createContext<Room | null>(null);

// LoginLayout is the frame every sign-in page stands in (port design §9): the sea and the pitch on one side, the
// brand, the language switch, and the side the page's question goes in. It is made once for all of them, so going from
// one to another (signing in to registering, the password to the second factor) changes only the question: the sea,
// the solid and the sharks stay where they are. The sea, the solid its sharks form for the step and the title's decode
// on an arrival from the website come from @axiom/fx/login, loaded after the first content.
export function LoginLayout() {
  const { t, i18n } = useTranslation();
  const site = useQuery(siteQuery);
  // the terms a registration accepts, read with the frame: a page gone on to that asks for them (registering) has them
  // already, its button ready from its first frame rather than grey until they come
  const legal = useQuery(legalQuery);
  const prefs = usePreferences();
  useBrandStyle(site.data?.brand_color);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const [standing, stand] = useState<Standing | null>(null);
  const [arrived] = useState(claimArrival);
  const engine = useLoginEngine(standing, prefs.dark, site.data?.brand_color, arrived);
  useArrivalReveal(engine.root, arrived, engine.started && !site.isPending && !legal.isPending && standing !== null);
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const name = site.data?.name ?? t("brand", { ns: "common" });
  const phone = useBoardMedia(loginPhoneWidth);
  const room = useMemo(() => ({ slot, stand }), [slot]);
  return (
    <RoomContext value={room}>
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
        {/* the page's question goes here (LoginFrame): a plain block, as the form panel lays out no child by name */}
        <div ref={setSlot} />
        <LoginDecode t={String} values={{ canvas: engine.decodeCanvas }} />
      </LoginShell>
      <Outlet />
    </RoomContext>
  );
}

// LoginFrame is a sign-in page's question as the Login board draws it, in the frame LoginLayout keeps: its title and
// line over the fields, in a form that submits through onSubmit, and the cancel of a request that is out.
export function LoginFrame({ title, sub, subStyle, step, shape, attempt, stage, answer, onSubmit, children }: Frame) {
  const { t } = useTranslation();
  const room = use(RoomContext);
  usePageTitle(title);
  const form = useRef<HTMLFormElement | null>(null);
  const cancel = useRef<HTMLDivElement | null>(null);
  const locked = attempt.kind !== null;
  useLock(form, cancel, locked);
  const titleRef = useRef<HTMLElement | null>(null);
  useFocusOnStep(form, step, titleRef, answer === true && !locked);
  const stand = room?.stand;
  useLayoutEffect(
    () => stand?.({ title, step, shape, stage, locked, titleRef }),
    [stand, title, step, shape, stage, locked],
  );
  if (!room?.slot) return null;
  return createPortal(
    <>
      <form ref={form} noValidate onSubmit={onSubmit} aria-busy={locked}>
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
    </>,
    room.slot,
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
// a keyboard or a screen reader is where the question is; the first step keeps the page's own, unless it is a request's
// answer (useFocusAnswer).
function useFocusOnStep(
  form: RefObject<HTMLFormElement | null>,
  step: string,
  title: RefObject<HTMLElement | null>,
  answered: boolean,
) {
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    firstControl(form.current)?.focus();
  }, [form, step]);
  useFocusAnswer(title, answered);
}

// A step that is a request's answer took the place of the form that sent it, and with it the focus the submit had.
// Once the page is free (answered: useLock gives the focus back first, to a control of the old form that is gone), the
// focus goes to the step's title, so a keyboard or a screen reader is where the answer is; once only, so a later
// request in the same step keeps its own.
function useFocusAnswer(title: RefObject<HTMLElement | null>, answered: boolean) {
  const given = useRef(false);
  useEffect(() => {
    const heading = title.current;
    if (!answered || given.current || !heading) return;
    given.current = true;
    heading.tabIndex = -1;
    heading.focus();
  }, [title, answered]);
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

// useLoginEngine runs the sea, the solid and an arrival's decode on the frame once their code has loaded, and keeps
// them on the page's shape and colours (the scheme, the site's brand). The decode ends when the title it plays over
// changes (another step, another page, another language). It hands the running engine to the stage of the page on show.
function useLoginEngine(standing: Standing | null, dark: boolean, brand: string | undefined, arrived: boolean) {
  const veil = useArrivalVeil(standing?.titleRef, arrived);
  const [started, setStarted] = useState(false);
  const root = useRef<HTMLElement | null>(null);
  useReloadWhenRestored();
  useReturnHome(root, standing?.locked ?? false);
  const decode = useRef<HTMLCanvasElement | null>(null);
  const latest = useRef({ standing, dark });
  latest.current = { standing, dark };
  const login = useEffectModule(
    root,
    loadLogin,
    (module, frame) => {
      const { standing: now, dark: night } = latest.current;
      const running = module.startLogin(frame, !night, now?.shape ?? "start");
      if (now) {
        for (const heard of now.stage.said.current.values()) running.hear(heard);
        now.stage.running.current = running;
      }
      setStarted(true);
      return running;
    },
    // an arrival waits for the sea to show the frame whole (useArrivalReveal), so its code is asked for at once
    arrived,
  );
  useStageOnShow(login, standing?.stage);
  // an arrival's decode plays once the engine runs and the first page's title is there, whichever comes last: an
  // arrival asks for the engine at once, so it may run before the page's question is in the frame
  const titleRef = standing?.titleRef;
  useEffect(() => {
    const running = login.current;
    const title = titleRef?.current;
    if (!started || !running || !title || !decode.current || !veil.on.current) return;
    veil.on.current = false;
    running.decode(title, decode.current);
  }, [started, titleRef, login, veil]);
  const shape = standing?.shape;
  useEffect(() => {
    if (shape) login.current?.show(shape);
  }, [login, shape]);
  // brand is a dependency only: useBrandStyle, an effect before this one, has put its colours on the page
  useEffect(() => login.current?.recolour(!dark), [login, dark, brand]);
  // title and step are dependencies only: when either changes, the veil and the decode of the old title end
  const title = standing?.title;
  const step = standing?.step;
  useEffect(() => {
    // the first page's title coming is no change of title
    if (title === undefined) return;
    return () => {
      veil.lift();
      login.current?.endDecode();
    };
  }, [login, veil, title, step]);
  return {
    root,
    started,
    seaCanvas: (canvas: Element | null) => {
      root.current = canvas?.closest<HTMLElement>(".lg") ?? null;
    },
    decodeCanvas: (element: Element | null) => {
      decode.current = element as HTMLCanvasElement | null;
    },
  };
}

// The page on show tells the running engine what its forms hear, and an engine already running hears what they said
// before they were shown (another page gone to in the frame).
function useStageOnShow(login: RefObject<Login | null>, stage: Stage | undefined) {
  useEffect(() => {
    const running = login.current;
    if (!stage) return;
    if (running) for (const heard of stage.said.current.values()) running.hear(heard);
    stage.running.current = running;
    return () => {
      stage.running.current = null;
    };
  }, [login, stage]);
}

// An arrival from the website shows the frame whole: the sea, the language switch and the side with the brand, the
// pitch and the question wait unseen for the sea's code, the site's settings, the terms and the page's question, at
// most revealFor, and then come in together. The window goes from the page's ground, where the departure left it, to
// the page, rather than through its parts one by one (an empty frame, the form, its button's colour, the sea).
const revealFor = 1500;
const revealIn = 240;
function useArrivalReveal(root: RefObject<HTMLElement | null>, arrived: boolean, ready: boolean) {
  const show = useRef(() => {});
  useLayoutEffect(() => {
    const frame = root.current;
    if (!arrived || !frame) return;
    const parts = [...frame.children].filter((part) => part instanceof HTMLElement);
    for (const part of parts) part.style.opacity = "0";
    let shown = false;
    let settled = 0;
    const lift = () => {
      if (shown) return;
      shown = true;
      window.clearTimeout(timer);
      for (const part of parts) {
        part.style.transition = `opacity ${revealIn}ms ease-out`;
        part.style.removeProperty("opacity");
      }
      // the parts' own opacity is the return's to fade (departLogin), at its own pace
      settled = window.setTimeout(() => {
        for (const part of parts) part.style.removeProperty("transition");
      }, revealIn);
    };
    const timer = window.setTimeout(lift, revealFor);
    show.current = lift;
    return () => {
      lift();
      window.clearTimeout(settled);
      for (const part of parts) part.style.removeProperty("transition");
    };
  }, [arrived, root]);
  useEffect(() => {
    if (ready) show.current();
  }, [ready]);
}

// An arrival's title (the first page's) is hidden from its first frame until the decode resolves it, or for veilFor if
// the decode's code is late; the colour goes through the CSSOM (CSP), and the decode gives each character's back. The
// veil lifts at once when its title goes (lift): a decode that comes later does not play then.
function useArrivalVeil(titleRef: RefObject<HTMLElement | null> | undefined, arrived: boolean) {
  const on = useRef(false);
  const end = useRef(() => {});
  // the arrival is the first page's: a page gone on to later in the frame shows its title as it is
  const spent = useRef(false);
  useLayoutEffect(() => {
    if (!arrived || !titleRef || spent.current) return;
    spent.current = true;
    const chars = titleRef.current?.querySelectorAll<HTMLElement>(".ch") ?? [];
    for (const c of chars) c.style.color = "transparent";
    on.current = true;
    const lift = () => {
      on.current = false;
      window.clearTimeout(timer);
      for (const c of chars) c.style.removeProperty("color");
    };
    const timer = window.setTimeout(lift, veilFor);
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
        // a space stays a space, so a title too long for one line breaks between its words
        <LoginChar key={`${i}${char}`} t={String} text={{ char }} />
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
