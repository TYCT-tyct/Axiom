import { useEffect, useState } from "react";
import CodeRegion from "./AxApp/regions/AdminCodeBlock.tsx";
import Track from "./AxApp/regions/AdminElapsedTrack.tsx";
import detail from "./AxApp/regions/LogsDetail.module.css";

// The page supplies formatted code so a JSON reader can preserve the original numbers.
export function CodeBlock({ label, children }: { label: string; children: string }) {
  return (
    <div>
      {/* The board's log detail sets the height bound. Its own parent keeps the panel in the page flow. */}
      <div className={`lgd ${detail.scope}`} tabIndex={-1}>
        <CodeRegion t={String} values={{ label }}>
          {children}
        </CodeRegion>
      </div>
    </div>
  );
}

// Only the waiting message is live. The deadline meter's current value is available on demand.
export function TestProgress({
  label,
  limitSeconds,
  formatElapsed,
}: {
  label: string;
  limitSeconds: number;
  formatElapsed: (seconds: number) => string;
}) {
  const [started] = useState(() => Date.now());
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => setElapsed(Math.max(0, Math.floor((Date.now() - started) / 1000))), 1000);
    return () => clearInterval(timer);
  }, [started]);
  const limit = Math.max(1, limitSeconds);
  const value = Math.min(elapsed, limit);
  const elapsedText = formatElapsed(elapsed);
  return (
    <div>
      <div role="status">{label}</div>
      <div aria-hidden="true">{elapsedText}</div>
      <Track
        t={String}
        values={{
          label,
          fill: `width: ${(value / limit) * 100}%`,
          meter: (element) => {
            element?.setAttribute("aria-valuemin", "0");
            element?.setAttribute("aria-valuemax", String(limit));
            element?.setAttribute("aria-valuenow", String(value));
            element?.setAttribute("aria-valuetext", elapsedText);
          },
        }}
      />
    </div>
  );
}
