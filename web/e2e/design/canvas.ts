import type { Page } from "@playwright/test";

// Replace the backing pixels, never the element's rectangle: a cover would also excuse overlaid text and glass.
// Preserve the context's alpha mode: zero alpha in an opaque sea creates invalid compositor pixels under invert().
// putImageData ignores the engine's transform, clip, alpha and blend mode; saving pixels leaves all that state intact.
export async function blankCanvases(page: Page, selectors: string[]) {
  return page.evaluateHandle((selectors) => {
    const canvases = new Set(
      selectors.flatMap((selector) => {
        const elements = [...document.querySelectorAll(selector)];
        if (!elements.length) throw new Error(`canvas ruling: ${selector} matches nothing`);
        return elements;
      }),
    );
    const saved = [...canvases].map((canvas) => {
      if (!(canvas instanceof HTMLCanvasElement)) throw new Error("canvas ruling: selector matches non-canvas");
      const ctx = canvas.getContext("2d");
      if (!ctx) throw new Error("canvas ruling requires a 2D canvas");
      const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height);
      const blank = ctx.createImageData(pixels.width, pixels.height);
      if (!ctx.getContextAttributes().alpha) {
        for (let i = 3; i < blank.data.length; i += 4) blank.data[i] = 255;
      }
      return { ctx, pixels, blank };
    });
    for (const { ctx, blank } of saved) ctx.putImageData(blank, 0, 0);
    return () => {
      for (const { ctx, pixels, blank } of saved) {
        const current = ctx.getImageData(0, 0, pixels.width, pixels.height);
        const changed = current.data.some((value, i) => value !== blank.data[i]);
        ctx.putImageData(pixels, 0, 0);
        if (changed) throw new Error("animated canvas repainted during screenshot capture");
      }
    };
  }, selectors);
}
