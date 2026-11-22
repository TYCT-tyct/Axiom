import type { Messages } from "@axiom/ui";
import { en as authEn, zh as authZh } from "./auth";
import { en as legalEn, zh as legalZh } from "./legal";

export const zh = { auth: authZh, legal: legalZh };
export const en: Messages<typeof zh> = { auth: authEn, legal: legalEn };
export type LoginMessages = typeof zh;
