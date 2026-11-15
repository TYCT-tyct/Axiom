// Motion that CSS cannot express on its own. Every CSS motion of the redesign sits under
// @media (prefers-reduced-motion: no-preference); a script that animates asks reducedMotion first.
export const reducedMotion = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// settled resolves once the element's own animations (an exit animation its leaving class started) have finished or
// been cancelled, and at once when it has none, as under reduced motion. Only its own: an endless one inside it, such
// as a spinner, would never let it go.
export function settled(element: Element) {
  return Promise.allSettled(element.getAnimations().map((animation) => animation.finished));
}
