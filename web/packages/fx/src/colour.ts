// colour resolves a CSS colour (a token, var(), light-dark()) to what a canvas can take, as an element under root
// would be drawn in it.
export function colour(root: HTMLElement, value: string) {
  const probe = document.createElement("i");
  probe.style.color = value;
  root.append(probe);
  const resolved = getComputedStyle(probe).color;
  probe.remove();
  return resolved;
}
