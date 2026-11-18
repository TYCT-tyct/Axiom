import { createContext, useContext } from "react";
import type { Brand } from "../shell";

// The site's name, logo and API address as the home page asks for them, once: the frame reads them from getSite, and
// the screens below take them from here, so the page never shows two answers to the same question.
export type Site = { brand: Brand | undefined; name: string };
export const SiteContext = createContext<Site>({ brand: undefined, name: "" });
export const useSite = () => useContext(SiteContext);
