import { type ReactNode, type RefObject } from "react";
import { ResizableWorkspace, type WorkspaceConfig } from "./ResizableWorkspace";

// Preserve the existing paper layout's localStorage contract.
const config: WorkspaceConfig = {
  storageKey: "signalwatch.paper-layout.v1",
  orderKey: "paperFirst",
  primaryLabel: "论文内容",
  primaryShortLabel: "论文",
  assistantLabel: "AI 论文助手",
  primaryClassName: "paper-content",
  assistantClassName: "paper-assistant-panel",
  gridClassName: "paper-workspace",
};
export function ResizablePaperWorkspace({
  enabled,
  paper,
  assistant,
  view,
  onViewChange,
  readingRef,
  assistantRef,
}: {
  enabled: boolean;
  paper: ReactNode;
  assistant: ReactNode;
  view: "paper" | "assistant";
  onViewChange: (view: "paper" | "assistant") => void;
  readingRef: RefObject<HTMLElement | null>;
  assistantRef: RefObject<HTMLElement | null>;
}) {
  return (
    <ResizableWorkspace
      config={config}
      enabled={enabled}
      primary={paper}
      assistant={assistant}
      view={view === "paper" ? "primary" : "assistant"}
      onViewChange={(value) =>
        onViewChange(value === "primary" ? "paper" : "assistant")
      }
      primaryRef={readingRef}
      assistantRef={assistantRef}
    />
  );
}
