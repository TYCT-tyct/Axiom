// Property-level CSSOM style tracker shared by fly.ts, page-write.ts and tiles.ts (trans.js.txt:132, 244–245):
// snapshots each element's original property values and whether it carried a style attribute so restore() puts every
// touched element back byte-for-byte without ever writing cssText or setAttribute("style") (CSP).
type SavedProp = { value: string; priority: string };
type Entry = { hadAttr: boolean; props: Map<string, SavedProp> };

export type StyleTracker = {
  // Suppresses CSS transitions on first touch so frame-driven values never lag behind the clock.
  set(el: HTMLElement, props: Record<string, string>): void;
  // Writes one property without adding transition:none (used for character color masks).
  setRaw(el: HTMLElement, prop: string, value: string): void;
  // Reverts one previously touched property to its saved value without releasing the rest of el.
  restoreProp(el: HTMLElement, prop: string): void;
  // Reverts every touched property on every element and drops empty style attributes this tracker introduced.
  restore(): void;
};

function applySaved(el: HTMLElement, prop: string, saved: SavedProp) {
  if (saved.value) el.style.setProperty(prop, saved.value, saved.priority);
  else el.style.removeProperty(prop);
}

export function styleTracker(): StyleTracker {
  const touched = new Map<HTMLElement, Entry>();

  const entryOf = (el: HTMLElement): Entry => {
    let entry = touched.get(el);
    if (!entry) {
      entry = { hadAttr: el.hasAttribute("style"), props: new Map() };
      touched.set(el, entry);
    }
    return entry;
  };

  const write = (el: HTMLElement, prop: string, value: string) => {
    const entry = entryOf(el);
    if (!entry.props.has(prop)) {
      entry.props.set(prop, {
        value: el.style.getPropertyValue(prop),
        priority: el.style.getPropertyPriority(prop),
      });
    }
    el.style.setProperty(prop, value);
  };

  return {
    set(el, props) {
      const entry = entryOf(el);
      if (!entry.props.has("transition")) write(el, "transition", "none");
      for (const [prop, value] of Object.entries(props)) write(el, prop, value);
    },
    setRaw: write,
    restoreProp(el, prop) {
      const saved = touched.get(el)?.props.get(prop);
      if (saved) applySaved(el, prop, saved);
    },
    restore() {
      for (const [el, entry] of touched) {
        for (const [prop, saved] of entry.props) applySaved(el, prop, saved);
        if (!entry.hadAttr && el.style.length === 0) el.removeAttribute("style");
      }
      touched.clear();
    },
  };
}
