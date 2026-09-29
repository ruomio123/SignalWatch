import { createContext, useContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import katex from "katex";
import "katex/dist/katex.min.css";
import { safeURL } from "./Common";

export type PaperCitation = {
  id: string;
  page: number;
  quote: string;
  url: string;
  content_hash?: string;
  source_type?: "html";
  source_version?: string;
  source_hash?: string;
  parser_version?: string;
  anchor?: string;
  label?: string;
  kind?: "table" | "formula";
  table?: { headers: string[]; rows: string[][]; caption: string; notes: string[] };
  formula?: { tex: string; context: string };
};

function EvidenceFormula({ tex }: { tex: string }) {
  const target = useRef<HTMLDivElement>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const element = target.current;
    if (!element) return;
    element.replaceChildren();
    try {
      if (new TextEncoder().encode(tex).length > 16_384) throw new Error("formula size");
      katex.render(tex, element, {
        displayMode: true,
        throwOnError: true,
        trust: false,
        strict: "error",
        maxExpand: 200,
        maxSize: 10,
        // KaTeX may mutate macros through \\gdef. Never share them with another source.
        macros: {},
        globalGroup: false,
      });
      setFailed(false);
    } catch {
      element.replaceChildren();
      setFailed(true);
    }
  }, [tex]);
  return (
    <>
      <div className="paper-evidence-formula" ref={target} hidden={failed} />
      {failed && <pre className="paper-evidence-formula-fallback"><code>{tex}</code></pre>}
    </>
  );
}

function StructuredEvidence({ reference }: { reference: PaperCitation }) {
  const { table, formula } = reference;
  if (reference.kind === "table" && table) {
    return <>
      <div className="paper-evidence-table" role="region" aria-label={table.caption || "原文表格"} tabIndex={0}>
        <table>
          {table.caption && <caption>{table.caption}</caption>}
          <thead><tr>{table.headers.map((header, index) => <th scope="col" key={index}>{header}</th>)}</tr></thead>
          <tbody>{table.rows.map((row, index) => <tr key={index}>{row.map((cell, column) => <td key={column}>{cell}</td>)}</tr>)}</tbody>
        </table>
      </div>
      {table.notes.length > 0 && <ul className="paper-evidence-notes">{table.notes.map((note, index) => <li key={index}>{note}</li>)}</ul>}
    </>;
  }
  if (reference.kind === "formula" && formula) {
    return <>
      <EvidenceFormula tex={formula.tex} />
      {formula.context && <p className="paper-evidence-context">{formula.context}</p>}
    </>;
  }
  return null;
}

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
              证据 {ref.id} · {ref.source_type === "html" ? "HTML 原文" : ref.page ? `第 ${ref.page} 页` : "摘要"}
              {ref.label && ` · ${ref.label}`}
            </summary>
            <StructuredEvidence reference={ref} />
            <blockquote>{ref.quote}</blockquote>
            {ref.source_version && <small>来源版本：{ref.source_version}</small>}
            <a href={safeURL(ref.url)} target="_blank" rel="noreferrer">
              查看 arXiv 原文
            </a>
          </details>
        ))}
    </>
  );
}
