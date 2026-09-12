import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type RefObject,
} from "react";
import {
  ArrowUp,
  KeyRound,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  X,
} from "lucide-react";
import { IconButton, trapDialogFocus } from "./Common";

/** One persistent dialog node keeps chat and editor state across layout changes. */
export function SubscriptionAssistantPanel({
  children,
  onClose,
}: {
  children: ReactNode;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [fullscreen, setFullscreen] = useState(false);
  useLayoutEffect(() => {
    const container = ref.current?.parentElement;
    if (!container) return;
    const measure = () =>
      setFullscreen(container.getBoundingClientRect().width < 1080);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, []);
  useLayoutEffect(() => {
    const trigger =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    return () => {
      ref.current?.close();
      if (trigger?.isConnected) trigger.focus();
    };
  }, []);
  useLayoutEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    const focused = dialog.contains(document.activeElement)
      ? (document.activeElement as HTMLElement)
      : null;
    dialog.close();
    if (fullscreen) dialog.showModal();
    else dialog.show();
    focused?.focus({ preventScroll: true });
    if (!fullscreen) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = previous;
    };
  }, [fullscreen]);
  return (
    <dialog
      ref={ref}
      className={`subscription-assistant-panel${fullscreen ? " is-fullscreen" : ""}`}
      aria-label="订阅助手"
      aria-modal={fullscreen ? true : undefined}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onKeyDown={(e) => {
        if (fullscreen) trapDialogFocus(e);
        else if (
          e.key === "Escape" &&
          !document.querySelector("dialog:modal")
        ) {
          e.preventDefault();
          onClose();
        }
      }}
    >
      {children}
    </dialog>
  );
}

export function SubscriptionAssistantView({
  settings,
  notices,
  messages,
  status,
  composer,
  onClose,
}: {
  settings: ReactNode;
  notices: ReactNode;
  messages: ReactNode;
  status: ReactNode;
  composer: ReactNode;
  onClose?: () => void;
}) {
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsID = useId();
  return (
    <section className="subscription-assistant">
      <header className="subscription-assistant-header">
        <span className="subscription-assistant-mark">
          <Sparkles size={22} aria-hidden="true" />
        </span>
        <div className="subscription-assistant-heading">
          <h2>订阅助手</h2>
          <p>订阅配置模式 · 确认后才会创建</p>
        </div>
        <IconButton
          label="助手设置"
          aria-expanded={settingsOpen}
          aria-controls={settingsID}
          onClick={() => setSettingsOpen(!settingsOpen)}
        >
          <SlidersHorizontal size={18} aria-hidden="true" />
        </IconButton>
        <IconButton label="关闭订阅助手" onClick={onClose}>
          <X size={20} aria-hidden="true" />
        </IconButton>
      </header>
      <div className="subscription-assistant-scroll">
        <div
          className="subscription-assistant-settings"
          id={settingsID}
          hidden={!settingsOpen}
        >
          {settings}
        </div>
        {notices}
        {messages}
      </div>
      <div className="subscription-assistant-bottom">
        <div className="subscription-assistant-status">{status}</div>
        <p className="subscription-assistant-confirmation">
          <ShieldCheck size={16} aria-hidden="true" />
          助手只生成草案，确认后才会创建订阅。
        </p>
        {composer}
      </div>
    </section>
  );
}

export function SubscriptionComposer({
  inputRef,
  question,
  onChange,
  onSend,
  disabled,
  modelLabel,
  configurationStatus,
}: {
  inputRef: RefObject<HTMLTextAreaElement | null>;
  question: string;
  onChange: (question: string) => void;
  onSend: (event: FormEvent<HTMLFormElement>) => void;
  disabled: boolean;
  modelLabel: string;
  configurationStatus: string;
}) {
  const composing = useRef(false);
  return (
    <form className="subscription-composer" onSubmit={onSend}>
      <div className="subscription-composer-field">
        <textarea
          ref={inputRef}
          aria-label="你的问题"
          placeholder="描述研究方向、篇数或邮件偏好……"
          rows={3}
          maxLength={2000}
          value={question}
          onChange={(e) => onChange(e.target.value)}
          onCompositionStart={() => {
            composing.current = true;
          }}
          onCompositionEnd={() => {
            composing.current = false;
          }}
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !composing.current &&
              !e.nativeEvent.isComposing &&
              e.nativeEvent.keyCode !== 229
            ) {
              e.preventDefault();
              if (!disabled) e.currentTarget.form?.requestSubmit();
            }
          }}
        />
        <button
          className="button button-primary"
          aria-label="发送"
          title="发送"
          disabled={disabled}
        >
          <ArrowUp size={20} aria-hidden="true" />
        </button>
      </div>
      <div className="subscription-composer-meta">
        <span>
          <KeyRound size={14} aria-hidden="true" />
          {modelLabel ? `${modelLabel} · ` : ""}
          {configurationStatus}
        </span>
        <span>Enter 发送 · Shift+Enter 换行</span>
      </div>
    </form>
  );
}
