import Under from "@axiom/boards/Site/regions/Under";
import { nearView } from "@axiom/fx";
import { DataState } from "@axiom/ui";
import { type ComponentType, useEffect, useRef, useState } from "react";
import styles from "./screens.module.css";

type Screen = ComponentType<{ wide: boolean }>;

// The screens below the first, in the board's order (Site.template.html's sections, data-sec 1 to 6). Each one's code
// loads when its place nears the view; a screen still to come (S2b, S2c) has nothing to load and takes no place.
const slots: { sec: number; load?: () => Promise<Screen> }[] = [
  { sec: 1 },
  { sec: 2, load: () => import("./models-screen").then((m) => m.ModelsScreen) },
  { sec: 3, load: () => import("./steps-screen").then((m) => m.StepsScreen) },
  { sec: 4, load: () => import("./trust-screen").then((m) => m.TrustScreen) },
  { sec: 5 },
  { sec: 6 },
];

// Screens are the home page's screens below the first, inside the board's .under, which their rules select under.
export function Screens({ wide }: { wide: boolean }) {
  return (
    <Under t={() => ""}>
      {slots.map(({ sec, load }) => (load ? <Slot key={sec} sec={sec} load={load} wide={wide} /> : null))}
    </Under>
  );
}

type Loading =
  | { status: "pending" }
  | { status: "success"; screen: Screen }
  | { status: "error"; error: unknown; at: number };

// Until its screen's code is there, a slot holds a view's height, so the screens after it start below the view and
// load in turn as the reader comes near them. The place carries its section for the island (behaviour.ts) as
// data-place: the screen inside it has the board's data-sec too. Code that fails to arrive (the network dropped) leaves
// the failure in the place; retrying reloads the page, as a browser may keep a module's failed load for the document.
function Slot({ sec, load, wide }: { sec: number; load: () => Promise<Screen>; wide: boolean }) {
  const place = useRef<HTMLDivElement | null>(null);
  const [loading, setLoading] = useState<Loading>({ status: "pending" });
  useEffect(() => {
    let live = true;
    const cancel = nearView(place.current, () => {
      load().then(
        (screen) => live && setLoading({ status: "success", screen }),
        (error: unknown) => live && setLoading({ status: "error", error, at: Date.now() }),
      );
    });
    return () => {
      live = false;
      cancel();
    };
  }, [load]);
  const retry = () => window.location.reload();
  const Loaded = loading.status === "success" ? loading.screen : null;
  return (
    <div ref={place} data-place={sec}>
      {Loaded ? (
        <Loaded wide={wide} />
      ) : (
        <div className={styles.waiting}>
          {loading.status === "error" ? (
            <DataState
              query={{
                status: "error",
                data: undefined,
                error: loading.error,
                errorUpdatedAt: loading.at,
                refetch: retry,
              }}
            >
              {() => null}
            </DataState>
          ) : null}
        </div>
      )}
    </div>
  );
}
