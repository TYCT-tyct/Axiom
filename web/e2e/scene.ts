import type { Page } from "@playwright/test";
import type { MockData } from "./mock.ts";

// A screen of an app, reached the way a user reaches it. The accessibility and layout checks and the screenshots of the
// acceptance gate (docs/frontend.md) walk the same list for each app, so a new screen is added once.
export type Scene = { name: string; signedIn: boolean; open: (page: Page) => Promise<void> };

// An app's screens, and how to mock its API for one of them: with the data the scene is shown with, and the session it
// needs. brands names the screens the extreme brand colours are tried on.
export type Suite = {
  app: string;
  scenes: Scene[];
  prepare: (page: Page, scene: Scene, data?: MockData) => Promise<unknown>;
  brands: string[];
};

// A screen whose dialog opens after something is done on the page.
export function dialogScene(
  name: string,
  goto: (page: Page) => Promise<void>,
  open: (page: Page) => Promise<void>,
): Scene {
  return {
    name,
    signedIn: true,
    open: async (page) => {
      await goto(page);
      await open(page);
      await page.getByRole("dialog").waitFor();
    },
  };
}
