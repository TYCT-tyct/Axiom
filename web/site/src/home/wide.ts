import { useSyncExternalStore } from "react";

// The board draws the home page in two layouts: wide (Site.dc.html, 1440) and narrow (the phone's and both tablets').
// The stylesheet's own steps change at 1280 (its --margin and --dsp), so the wide layout starts there.
const query = () => matchMedia("(min-width: 1280px)");

function subscribe(listener: () => void) {
  const media = query();
  media.addEventListener("change", listener);
  return () => media.removeEventListener("change", listener);
}

export function useWide() {
  return useSyncExternalStore(subscribe, () => query().matches);
}
