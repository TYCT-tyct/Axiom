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

// What a revert lets go lands at once: a transition it lets run from the value the frames left (transition was off while
// they drove it), or an entrance animation it lets play again (animation was off), would show the element a second time
// after the run has already shown it whole.
function settle(el: HTMLElement, animated: boolean) {
  for (const a of el.getAnimations()) {
    const transition = "transitionProperty" in a;
    const entrance = animated && "animationName" in a && Number.isFinite(a.effect?.getComputedTiming().endTime);
    if (transition || entrance) a.finish();
  }
}

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
      for (const [el, entry] of touched) settle(el, entry.props.has("animation"));
      touched.clear();
    },
  };
}

// A page's own visibility and inertness before any run (the website's or the sign-in page's), so a run can hide and
// show it and put it back as it was.
export type PageInit = { vis: string; visPri: string; hadStyle: boolean; hadInert: boolean };

export function snapshotPage(el: HTMLElement): PageInit {
  return {
    vis: el.style.getPropertyValue("visibility"),
    visPri: el.style.getPropertyPriority("visibility"),
    hadStyle: el.hasAttribute("style"),
    hadInert: el.hasAttribute("inert"),
  };
}

export function setPageHidden(el: HTMLElement, init: PageInit, hidden: boolean) {
  if (hidden) {
    el.style.setProperty("visibility", "hidden");
    el.inert = true;
    el.toggleAttribute("inert", true);
    return;
  }
  if (init.vis === "hidden") el.style.setProperty("visibility", "visible");
  else if (init.vis) el.style.setProperty("visibility", init.vis, init.visPri);
  else {
    el.style.removeProperty("visibility");
    if (!init.hadStyle && el.style.length === 0) el.removeAttribute("style");
  }
  el.inert = false;
  el.toggleAttribute("inert", false);
}

export function restorePageInit(el: HTMLElement, init: PageInit) {
  if (init.vis) el.style.setProperty("visibility", init.vis, init.visPri);
  else el.style.removeProperty("visibility");
  if (!init.hadStyle && el.style.length === 0) el.removeAttribute("style");
  el.inert = init.hadInert;
  el.toggleAttribute("inert", init.hadInert);
}
