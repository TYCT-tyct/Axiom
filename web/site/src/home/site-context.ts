import type { Registration } from "@axiom/api";
import { createContext, useContext } from "react";
import type { Brand } from "../shell";

// How the site takes new users as far as the page knows: "unknown" until the site has answered, or while its call has
// failed, when the page promises only what holds for every site (signing in, the models).
export type Join = Registration | "unknown";

// The site's name, logo, API address and registration as the home page asks for them, once, above the layout (home.tsx):
// the screens take them from here, so the page never shows two answers to the same question.
export type Site = { brand: Brand | undefined; name: string; join: Join };
export const SiteContext = createContext<Site>({ brand: undefined, name: "", join: "unknown" });
export const useSite = () => useContext(SiteContext);
