import Mark from "@axiom/boards/Site/regions/Mark";
import { type RefObject, useLayoutEffect, useState } from "react";
import { createPortal } from "react-dom";
import styles from "./brand.module.css";

// The site's logo and name where the board draws Axiom's (docs/frontend.md「主题与自由度」): the board's mark when the
// site has no logo or its logo fails to load, as Logo does in the console. Without a name (the island) only the mark.
export function Brand({ logoUrl, name }: { logoUrl?: string; name?: string }) {
  // The URL that failed, not a boolean: a new logo at a new URL is tried, not hidden behind the old failure.
  const [failed, setFailed] = useState<string | null>(null);
  return (
    <>
      {logoUrl && failed !== logoUrl ? (
        <img className={styles.logo} src={logoUrl} alt="" width="20" height="20" onError={() => setFailed(logoUrl)} />
      ) : (
        <Mark t={() => ""} />
      )}
      {name !== undefined && <span>{name}</span>}
    </>
  );
}

// BrandPlaces fills the brand's places in the frame root points at: the header's, the menu sheet's and the island's.
// The frame's region leaves them empty (packages/boards/manifest/site.ts), since a region has one slot and the frame's is
// the scroller; they are found once mounted, before the first paint.
export function BrandPlaces({
  root,
  logoUrl,
  name,
}: {
  root: RefObject<HTMLElement | null>;
  logoUrl?: string;
  name: string;
}) {
  const [places, setPlaces] = useState<Element[]>([]);
  useLayoutEffect(() => {
    setPlaces([...(root.current?.querySelectorAll(".bar .brand, .sheet .brand, .isl .im") ?? [])]);
  }, [root]);
  return places.map((place, i) =>
    createPortal(<Brand logoUrl={logoUrl} name={place.matches(".im") ? undefined : name} />, place, String(i)),
  );
}
