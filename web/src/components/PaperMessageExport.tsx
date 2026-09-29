import { useState } from "react";
import { paperMarkdown, type PaperExportContent } from "../lib/paper-export";

export function PaperMessageExport({ messageID, ...message }: PaperExportContent & { messageID: string }) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(paperMarkdown(message));
      setCopied(true);
      setError("");
    } catch {
      setError("复制失败，可点击“下载 Markdown”保存文件，或允许浏览器访问剪贴板后重试。");
    }
  }
  function download() {
    let url: string | undefined;
    try {
      url = URL.createObjectURL(new Blob([paperMarkdown(message)], { type: "text/markdown;charset=utf-8" }));
      const link = document.createElement("a");
      link.href = url;
      link.download = `signalwatch-paper-${messageID.replace(/[^a-zA-Z0-9_-]/g, "-")}.md`;
      link.click();
      setError("");
    } catch {
      setError("下载失败，请重试。");
    } finally {
      if (url) setTimeout(() => URL.revokeObjectURL(url!), 0);
    }
  }
  return <div className="paper-message-export">
    <div className="react-actions">
      <button type="button" className="button" onClick={() => void copy()}>复制 Markdown</button>
      <button type="button" className="button" onClick={download}>下载 Markdown</button>
      {copied && <span role="status">已复制 Markdown</span>}
    </div>
    {error && <p role="alert">{error}</p>}
  </div>;
}
