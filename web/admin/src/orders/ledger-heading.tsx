import { Link } from "@tanstack/react-router";

// The description and the way back shared by the ledger views of the orders page: cash movements and paid batches.
export function LedgerHeading({ description, back }: { description: string; back: string }) {
  return (
    <p className="px-4 pt-4 text-t2">
      {description}{" "}
      <Link to="/orders" className="text-accent underline-offset-4 hover:underline">
        {back}
      </Link>
    </p>
  );
}
