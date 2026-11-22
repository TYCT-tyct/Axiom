import First from "@axiom/boards/Site/regions/FirstScreen";
import Headline from "@axiom/boards/Site/regions/Headline";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { links } from "./links";
import { type BoardKey, words } from "./words";

// The first screen: the headline, the lede and the way on, as the board draws them. The words the preference
// engine rewrites in place are written once, in the language the page opened in (frame.tsx says why); the headline in
// Chinese is the board's own markup, its phrases and the key in italics, and in another language plain text, which the
// engine lays out the same way when it decodes into it.
export function FirstScreen() {
  const { t, i18n } = useTranslation();
  const [first] = useState(() => ({ t: i18n.getFixedT(i18n.language), zh: i18n.language === "zh" }));
  const opened = (key: BoardKey) => first.t(`board.${key}`);
  return (
    <First
      t={(key) => t(`board.${key}`)}
      text={{
        lede: opened(words.lede),
        signIn: opened(words.signInForKey),
        signInHref: links.signIn,
      }}
    >
      {first.zh ? <Headline t={opened} /> : opened(words.headline)}
    </First>
  );
}
