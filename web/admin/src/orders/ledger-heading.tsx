import { Text, TextLink } from "@axiom/ui";
import { Link } from "@tanstack/react-router";

// The description and the way back shared by the ledger views of the orders page: cash movements and paid batches.
export function LedgerHeading({ description, back }: { description: string; back: string }) {
  return (
    <Text tone="muted">
      {description}{" "}
      <TextLink>
        <Link to="/orders">{back}</Link>
      </TextLink>
    </Text>
  );
}
