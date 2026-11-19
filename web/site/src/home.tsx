import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { FirstScreen } from "./home/first-screen";
import { SiteFrame } from "./home/frame";
import { Screens } from "./home/screens";
import { SiteContext } from "./home/site-context";
import { useWide } from "./home/wide";
import { useSiteState } from "./shell";

// The website's home page (the owner's Site boards, #747): the sea, the header, the first screen and the screens below
// it (home/screens.tsx). A change of layout starts the frame afresh, its effects with it: they hold the old markup. The
// site's settings are asked for here, above the layout, so a change of layout keeps them and does not ask again.
export function Home() {
  const { t } = useTranslation();
  const wide = useWide();
  const site = useSiteState();
  const brand = site.status === "ready" ? site.brand : undefined;
  const name = brand?.name ?? t("brand", { ns: "common" });
  // whether the whale has come on its own: once per page load, whatever the layout
  const met = useRef(false);
  return (
    <SiteContext value={{ brand, name, join: brand?.registration ?? "unknown" }}>
      <SiteFrame key={wide ? "wide" : "narrow"} wide={wide} met={met}>
        <FirstScreen />
        <Screens wide={wide} />
      </SiteFrame>
    </SiteContext>
  );
}
