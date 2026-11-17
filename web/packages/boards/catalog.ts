export const boardNames = [
  "Login",
  "ClawdEgg",
  "AxApp",
  "Site",
  "SiteTablet",
  "SiteTabletWide",
  "SitePhone",
  "Models",
  "ModelsPhone",
  "Faq",
  "FaqPhone",
  "FaqTablet",
  "Steps",
  "ConnectV6",
  "ConnectV6Phone",
  "Bed",
  "BedPhone",
  "BedTablet",
  "LoginDark",
  "LoginPhone",
  "AxAppTablet",
  "AxAppPhone",
  "AxAppDark",
  "AxAppPhoneDark",
] as const;

export const engineNames = [
  "ocean-orig",
  "login-ui",
  "clawd",
  "clawd-egg",
  "ocean-react",
  "respond",
  "whale",
  "whale-egg",
  "clawd-cast",
  "trans",
  "prefs",
  "hero-enter",
  "eggs",
  "routes",
];

export function boardEngines(name: string, html: string): string[] {
  // Preserve the original three public components and their module graphs.
  if (name === "AxApp") return [];
  return [...html.matchAll(/<script[^>]*src="\.\/([\w-]+)\.js"/g)]
    .map((match) => match[1] as string)
    .filter((engine) => engine !== "support");
}
