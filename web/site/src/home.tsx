import { useTranslation } from "react-i18next";
import { FirstScreen } from "./home/first-screen";
import { SiteFrame } from "./home/frame";
import { Screens } from "./home/screens";
import { useWide } from "./home/wide";

// The website's home page (the owner's Site boards, #747): the sea, the header, the first screen and the screens below
// it (home/screens.tsx). A change of layout starts the frame afresh, its effects with it: they hold the old markup.
// The footer names the Chinese font, as MiSans's licence asks (docs/frontend.md「字体」), until the seabed screen's own
// footer replaces it.
export function Home() {
  const { t } = useTranslation();
  const wide = useWide();
  return (
    <SiteFrame key={wide ? "wide" : "narrow"} wide={wide}>
      <FirstScreen />
      <Screens wide={wide} />
      <footer>{t("footer")}</footer>
    </SiteFrame>
  );
}
