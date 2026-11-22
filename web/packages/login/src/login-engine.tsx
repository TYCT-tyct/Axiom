import { arrivedWhole, flyParts, held, useEffectModule } from "@axiom/fx";
import type { Login } from "@axiom/fx/login";
import { type RefObject, use, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { type LoginHost, LoginHostContext } from "./host";
import type { Stage, Standing } from "./login-frame";
import { useReturnHome } from "./return-home";

const loadLogin = () => import("@axiom/fx/login");

// How long an arrival's title waits, hidden, for the decode's code before it is shown as it is.
const veilFor = 1500;

// A page the browser shows again from its back-forward cache (back from a provider's page) is the page as the visitor
// left it, mid-request: it loads afresh instead, from the server's session, rather than guess what became of that.
function useReloadWhenRestored() {
  useEffect(() => {
    const restored = (event: PageTransitionEvent) => event.persisted && window.location.reload();
    window.addEventListener("pageshow", restored);
    return () => window.removeEventListener("pageshow", restored);
  }, []);
}

// an arrival's decode plays once the engine runs and the first page's title is there, whichever comes last: an
// arrival asks for the engine at once, so it may run before the page's question is in the frame
function useArrivalDecode(
  started: boolean,
  login: RefObject<Login | null>,
  titleRef: RefObject<HTMLElement | null> | undefined,
  decode: RefObject<HTMLCanvasElement | null>,
  veil: ReturnType<typeof useArrivalVeil>,
) {
  useEffect(() => {
    const running = login.current;
    const title = titleRef?.current;
    if (!started || !running || !title || !decode.current || !veil.on.current) return;
    veil.on.current = false;
    running.decode(title, decode.current);
  }, [started, titleRef, login, decode, veil]);
}

// useLoginEngine runs the sea, the solid and an arrival's decode on the frame once their code has loaded, and keeps
// them on the page's shape and colours (the scheme, the site's brand). The decode ends when the title it plays over
// changes (another step, another page, another language). It hands the running engine to the stage of the page on show.
export function useLoginEngine(standing: Standing | null, dark: boolean, brand: string | undefined, arrived: boolean) {
  const host = use(LoginHostContext);
  const veil = useArrivalVeil(standing?.titleRef, arrived);
  const [started, setStarted] = useState(false);
  const root = useRef<HTMLElement | null>(null);
  useReloadWhenRestored();
  useReturnHome(root, standing?.locked ?? false, !host);
  const decode = useRef<HTMLCanvasElement | null>(null);
  const latest = useRef({ standing, dark });
  latest.current = { standing, dark };
  const login = useEffectModule(
    root,
    loadLogin,
    (module, frame) => {
      const { standing: now, dark: night } = latest.current;
      const shape = now?.shape ?? "start";
      // a host's sea is the solid's: the page's own canvas stays empty under it (LoginHost)
      const running = host?.sea
        ? module.joinLogin(host.root, frame, host.sea, shape, host.way)
        : module.startLogin(frame, !night, shape);
      if (now) {
        for (const heard of now.stage.said.current.values()) running.hear(heard);
        now.stage.running.current = running;
      }
      setStarted(true);
      return running;
    },
    // an arrival waits for the sea to show the frame whole (useArrivalReveal), and a host's transition for the solid
    // (useHostReady), so its code is asked for at once
    arrived || host !== null,
  );
  // a host readies the frame before its page is chosen: laid out with the solid on its sea, it may start the
  // transition, and the page's question comes in while it runs (tiles.ts)
  useHostReady(host, root, started);
  useStageOnShow(login, standing?.stage);
  useArrivalDecode(started, login, standing?.titleRef, decode, veil);
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
      // a host's sea shows through the frame where the page's own would be
      if (host?.sea && canvas instanceof HTMLElement) canvas.style.display = "none";
    },
    decodeCanvas: (element: Element | null) => {
      decode.current = element as HTMLCanvasElement | null;
    },
  };
}

// A host starts its transition once the frame is laid out with its question and the solid is on its sea: once only,
// for the first page; going on to another step in the frame is no new arrival.
function useHostReady(host: LoginHost | null, root: RefObject<HTMLElement | null>, ready: boolean) {
  const told = useRef(false);
  useEffect(() => {
    const frame = root.current;
    if (!host || !ready || !frame || told.current) return;
    told.current = true;
    host.ready(frame);
  }, [host, root, ready]);
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
export function useArrivalReveal(root: RefObject<HTMLElement | null>, arrived: boolean, ready: boolean) {
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
      // under the website still held on screen (crossing.ts) the view transition's cross-fade brings the page in, so
      // its parts are shown at once; the mark slides over from the website's place
      const fade = !held();
      for (const part of parts) {
        if (fade) part.style.transition = `opacity ${revealIn}ms ease-out`;
        part.style.removeProperty("opacity");
      }
      arrivedWhole();
      flyParts({ brand: frame.querySelector<HTMLElement>("a.brand") });
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
    if (!ready) return;
    show.current();
    // a page come to any other way is whole too: the page left for it cross-fades into it now (crossing.ts)
    arrivedWhole();
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
