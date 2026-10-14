import { expect, it } from "vitest";
import { cn } from "./cn";

it("keeps a design text size and a text colour together, and lets a later size win", () => {
  expect(cn("text-ui text-btn-ink", "text-label")).toBe("text-btn-ink text-label");
});
