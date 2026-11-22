import { useEffect, useRef, useState } from "react";
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
  const wide = useLayout();
  const site = useSiteState();
  const brand = site.status === "ready" ? site.brand : undefined;
  const name = brand?.name ?? t("brand", { ns: "common" });
  // whether the whale has come on its own: once per page load, whatever the layout
  const met = useRef(false);
  return (
    <SiteContext value={{ brand, name, join: brand?.registration ?? "unknown" }}>
      <SiteFrame key={wide.wide ? "wide" : "narrow"} wide={wide.wide} met={met} onSigning={wide.hold}>
        <FirstScreen />
        <Screens wide={wide.wide} />
      </SiteFrame>
    </SiteContext>
  );
}

// The layout the frame is drawn in: the window's, but held while the sign-in pages are up or on their way (sign-in.tsx).
// Starting the frame afresh would take them, the transition and the sea they stand on with it; they follow the window
// themselves, and the homepage takes the new layout once the visitor is home again.
function useLayout() {
  const live = useWide();
  const [signing, hold] = useState(false);
  const [wide, setWide] = useState(live);
  useEffect(() => {
    if (!signing) setWide(live);
  }, [live, signing]);
  return { wide: signing ? wide : live, hold };
}
