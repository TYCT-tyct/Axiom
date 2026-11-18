import type { Page } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import type { MockData } from "./mock.ts";
import { pageHeading } from "./page-heading.ts";
import type { Scene, Suite } from "./scene.ts";
import { words } from "./words.ts";

// The screens of the website. Its first screen asks the API for nothing; the console's mocks answer what else it might.
const home: Scene = {
  name: "home",
  signedIn: false,
  open: async (page) => {
    await page.goto(apps[0].url);
    await pageHeading(page);
  },
};

const models: Scene = {
  name: "models",
  signedIn: false,
  open: async (page) => {
    await page.goto(`${apps[0].url}models`);
    await page.getByRole("table", { name: words("publicModels.title") }).waitFor();
  },
};

export const siteSuite: Suite = {
  app: "site",
  scenes: [home, models],
  prepare: (page: Page, _scene: Scene, data: MockData = "usual") => mockConsole(page, data),
  brands: [],
};
