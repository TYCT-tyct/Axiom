import type { AdminSchemas } from "@axiom/api";
import { Cluster, Tag } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { Code } from "../parts";
import { belowFloor } from "./floor";

type Publication = AdminSchemas["PricePublication"];

// What a publication is, in the words of the sale tab: a price that is up to date, one waiting to be published, one the
// rule asks below the site's floor, or one the server cannot make.
type Standing = "current" | "waiting" | "floor" | "blocked" | "unpriced";

export function standingOf(publication: Publication): Standing {
  if (publication.status === "blocked") return "blocked";
  if (publication.status === "unpriced") return "unpriced";
  if (publication.status === "published") return "current";
  return belowFloor(publication) ? "floor" : "waiting";
}

const tones = { current: "neutral", waiting: "accent", floor: "bad", blocked: "bad", unpriced: "warn" } as const;

// Standings is the state of a row, and the reason the server gives when it has one: why a proposal cannot be made.
export function Standings({ row }: { row: Publication }) {
  const { t } = useTranslation();
  const standing = standingOf(row);
  return (
    <Cluster gap={8}>
      <Tag tone={tones[standing]} dot>
        {t(`pricing.sale.standing.${standing}`)}
      </Tag>
      {row.problem ? <Code>{row.problem}</Code> : null}
    </Cluster>
  );
}
