import { LoadingState } from "@axiom/ui";
import { type ReactNode, Suspense } from "react";

// Lazily holds a part loaded on its own (a tab, a dialog) until its chunk is in, showing the loading state meanwhile.
export function Lazily({ children }: { children: ReactNode }) {
  return <Suspense fallback={<LoadingState />}>{children}</Suspense>;
}
