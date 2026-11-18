import Under from "@axiom/boards/Site/regions/Under";
import { nearView, useEffectModule } from "@axiom/fx";
import { type ComponentType, useRef, useState } from "react";
import styles from "./screens.module.css";

type Screen = ComponentType<{ wide: boolean }>;

// The screens below the first, in the board's order (Site.template.html's sections). Each one's code loads when its
// place nears the view; a screen still to come (S2b, S2c) has nothing to load and takes no place.
const slots: { name: string; load?: () => Promise<Screen> }[] = [
  { name: "connect" },
  { name: "models", load: () => import("./models-screen").then((m) => m.ModelsScreen) },
  { name: "steps", load: () => import("./steps-screen").then((m) => m.StepsScreen) },
  { name: "production", load: () => import("./trust-screen").then((m) => m.TrustScreen) },
  { name: "faq" },
  { name: "seabed" },
];

// Screens are the home page's screens below the first, inside the board's .under, which their rules select under.
export function Screens({ wide }: { wide: boolean }) {
  return (
    <Under t={() => ""}>
      {slots.map((slot) => (slot.load ? <Slot key={slot.name} load={slot.load} wide={wide} /> : null))}
    </Under>
  );
}

// Until its screen's code is there, a slot holds a view's height, so the screens after it start below the view and
// load in turn as the reader comes near them. A screen whose code fails to load leaves its place empty.
function Slot({ load, wide }: { load: () => Promise<Screen>; wide: boolean }) {
  const place = useRef<HTMLDivElement | null>(null);
  const [Loaded, setLoaded] = useState<Screen | null>(null);
  useEffectModule(
    place,
    load,
    (screen) => {
      setLoaded(() => screen);
      return { destroy: () => {} };
    },
    nearView,
  );
  return <div ref={place}>{Loaded ? <Loaded wide={wide} /> : <div className={styles.waiting} />}</div>;
}
