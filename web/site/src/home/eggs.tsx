import type { Sea } from "@axiom/fx/sea";
import { type RefObject, useEffect, useState } from "react";

const loadEggs = () => import("./easter-eggs");
type SeaEggs = Awaited<ReturnType<typeof loadEggs>>["SeaEggs"];

// Eggs loads the sea's easter eggs (easter-eggs.tsx) once the sea is up, which is after the first content: they play on
// it. A module that fails to load leaves the page as it is.
export function Eggs({
  root,
  sea,
  whale,
}: {
  root: RefObject<HTMLElement | null>;
  sea: Sea | null;
  whale: () => void;
}) {
  const [Loaded, setLoaded] = useState<SeaEggs | null>(null);
  const up = sea !== null;
  useEffect(() => {
    if (!up) return;
    let live = true;
    loadEggs().then(
      (module) => live && setLoaded(() => module.SeaEggs),
      () => {
        // the eggs are decoration: the page works without them
      },
    );
    return () => {
      live = false;
    };
  }, [up]);
  const frame = root.current;
  return Loaded && sea && frame ? <Loaded frame={frame} sea={sea} whale={whale} /> : null;
}
