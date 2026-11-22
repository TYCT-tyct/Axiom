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
import { takeArrival } from "@axiom/fx";
import type { Heard, Login, Shape } from "@axiom/fx/login";
import { BoardMark, boardMedia, useBoardMedia, useBrandStyle, usePageTitle, usePreferences } from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  type FormEvent,
  type ReactNode,
  type RefObject,
  use,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { Attempt } from "./attempt";
import { LoginHostContext } from "./host";
import { useLoginMessages } from "./index";
import { useArrivalReveal, useLoginEngine } from "./login-engine";
import { NoteAction } from "./login-parts";
import { legalQuery, siteQuery } from "./queries";
import { RevealHold } from "./reveal-hold";

// The Login board puts its sea above the form, as a band, below 768px.
const loginPhoneWidth = boardMedia("(max-width: 767px)");

// The departure's marker is taken by the first sign-in page this document shows, the page the departure opened: the
// next step (two-step verification, consent) is not an arrival.
let unclaimed = true;
function claimArrival() {
  if (!unclaimed) return false;
  unclaimed = false;
  return takeArrival();
}

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
export type Standing = {
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
export function LoginLayout({ children }: { children: ReactNode }) {
  useLoginMessages();
  const { t, i18n } = useTranslation("login");
  const site = useQuery(siteQuery);
  // the terms a registration accepts, read with the frame but not waited for: a page gone on to that asks for them
  // (registering) has them already, its button ready from its first frame rather than grey until they come, and a page
  // that shows none (signing in) does not wait for them (useHoldReveal). A frame the website holds waits, ready, on
  // every visit to the homepage, most with no press: its page asks for them when a press puts it in, and the transition
  // writes the question 620 ms later (tiles.ts)
  const client = useQueryClient();
  const hosted = use(LoginHostContext) !== null;
  useEffect(() => {
    if (!hosted) void client.prefetchQuery(legalQuery);
  }, [client, hosted]);
  const prefs = usePreferences();
  useBrandStyle(site.data?.brand_color);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const [standing, stand] = useState<Standing | null>(null);
  const [held, setHeld] = useState(0);
  const [arrived] = useState(claimArrival);
  const engine = useLoginEngine(standing, prefs.dark, site.data?.brand_color, arrived);
  useArrivalReveal(engine.root, arrived, engine.started && !site.isPending && standing !== null && held === 0);
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const name = site.data?.name ?? t("brand", { ns: "common" });
  const phone = useBoardMedia(loginPhoneWidth);
  const room = useMemo(() => ({ slot, stand }), [slot]);
  const hold = useCallback((by: 1 | -1) => setHeld((n) => n + by), []);
  return (
    <RoomContext value={room}>
      <RevealHold value={hold}>
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
        {children}
      </RevealHold>
    </RoomContext>
  );
}

// LoginFrame is a sign-in page's question as the Login board draws it, in the frame LoginLayout keeps: its title and
// line over the fields, in a form that submits through onSubmit, and the cancel of a request that is out.
export function LoginFrame({ title, sub, subStyle, step, shape, attempt, stage, answer, onSubmit, children }: Frame) {
  const { t } = useTranslation("login");
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
