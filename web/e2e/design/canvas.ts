import type { Page } from "@playwright/test";

// Replace the backing pixels, never the element's rectangle: a cover would also excuse overlaid text and glass.
// Zero pixels clear transparent layers (Clawd) and become black in opaque contexts (sea), preserving other DOM below.
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
      return { ctx, pixels };
    });
    for (const { ctx, pixels } of saved) {
      ctx.putImageData(ctx.createImageData(pixels.width, pixels.height), 0, 0);
    }
    return () => {
      for (const { ctx, pixels } of saved) ctx.putImageData(pixels, 0, 0);
    };
  }, selectors);
}
