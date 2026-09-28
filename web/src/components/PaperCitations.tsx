import { createContext, useContext, useId, type ReactNode } from "react";
import { safeURL } from "./Common";

export type PaperCitation = {
  id: string;
  page: number;
  quote: string;
  url: string;
};

type CitationContextValue = {
  references: PaperCitation[];
  targetID: (id: string) => string;
};
const CitationContext = createContext<CitationContextValue | undefined>(undefined);

export function PaperCitations({
  messageID,
  citations,
  children,
}: {
  messageID: string;
  citations: PaperCitation[];
  children: ReactNode;
}) {
  // Message identity remains part of the target. The mounted scope also keeps
  // independent views and replaced sessions from sharing a citation target.
  const instance = useId();
  const references = [...new Map(citations.map((ref) => [ref.id, ref])).values()];
  const targetID = (id: string) =>
    `paper-evidence-${instance}-${encodeURIComponent(JSON.stringify([messageID, id]))}`;
  return (
    <CitationContext.Provider value={{ references, targetID }}>
      {children}
    </CitationContext.Provider>
  );
}

export function CitationMarker({ id }: { id: string }) {
  const scope = useContext(CitationContext);
  if (!scope?.references.some((ref) => ref.id === id)) return <>[{id}]</>;
  const target = scope.targetID(id);
  return (
    <button
      type="button"
      className="paper-citation-marker"
      aria-label={`查看证据 ${id}`}
      aria-controls={target}
      onClick={() => {
        const details = document.getElementById(target);
        if (!(details instanceof HTMLDetailsElement)) return;
        details.open = true;
        details.querySelector("summary")?.focus({ preventScroll: true });
        details.scrollIntoView({ block: "nearest", inline: "nearest" });
      }}
    >
      [{id}]
    </button>
  );
}

// Legacy content remains literal text. Only bracketed IDs that belong to the
// current message become controls; HTML and Markdown links are never parsed.
export function CitationText({ text }: { text: string }) {
  const parts = text.split(/(\[[^\]\r\n]+\])/g);
  return (
    <>
      {parts.map((part, index) =>
        part.startsWith("[") && part.endsWith("]") ? (
          <CitationMarker key={index} id={part.slice(1, -1)} />
        ) : part,
      )}
    </>
  );
}

export function CitationDetails({ ids }: { ids?: string[] }) {
  const scope = useContext(CitationContext);
  if (!scope) return null;
  return (
    <>
      {scope.references
        .filter((ref) => !ids || ids.includes(ref.id))
        .map((ref) => (
          <details
            key={ref.id}
            id={scope.targetID(ref.id)}
            className="paper-citation-detail"
          >
            <summary>
              证据 {ref.id} · {ref.page ? `第 ${ref.page} 页` : "摘要"}
            </summary>
            <blockquote>{ref.quote}</blockquote>
            <a href={safeURL(ref.url)} target="_blank" rel="noreferrer">
              查看 arXiv 原文
            </a>
          </details>
        ))}
    </>
  );
}
