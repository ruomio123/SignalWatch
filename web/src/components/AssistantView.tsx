import {
  useId,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type RefObject,
} from "react";
import {
  ArrowUp,
  KeyRound,
  SlidersHorizontal,
  Sparkles,
  X,
} from "lucide-react";
import { IconButton } from "./Common";

export function AssistantView({
  title,
  subtitle,
  hint,
  settings,
  notices,
  messages,
  status,
  composer,
  onClose,
  scrollRef,
}: {
  title: string;
  subtitle: string;
  hint?: ReactNode;
  settings: ReactNode;
  notices: ReactNode;
  messages: ReactNode;
  status: ReactNode;
  composer: ReactNode;
  onClose?: () => void;
  scrollRef?: RefObject<HTMLDivElement | null>;
}) {
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsID = useId();
  return (
    <section className="assistant">
      <header className="assistant-header">
        <span className="assistant-mark">
          <Sparkles size={22} aria-hidden="true" />
        </span>
        <div className="assistant-heading">
          <h2>{title}</h2>
          <p title={subtitle}>{subtitle}</p>
        </div>
        <IconButton
          label="助手设置"
          aria-expanded={settingsOpen}
          aria-controls={settingsID}
          onClick={() => setSettingsOpen(!settingsOpen)}
        >
          <SlidersHorizontal size={18} aria-hidden="true" />
        </IconButton>
        <IconButton label={`关闭${title}`} onClick={onClose}>
          <X size={20} aria-hidden="true" />
        </IconButton>
      </header>
      <div className="assistant-scroll" ref={scrollRef}>
        <div
          className="assistant-settings"
          id={settingsID}
          hidden={!settingsOpen}
        >
          {settings}
        </div>
        {notices}
        {messages}
      </div>
      <div className="assistant-bottom">
        <div className="assistant-status">{status}</div>
        {hint && <p className="assistant-hint">{hint}</p>}
        {composer}
      </div>
    </section>
  );
}

export function AssistantComposer({
  inputRef,
  question,
  onChange,
  onSend,
  disabled,
  inputDisabled = false,
  modelLabel,
  configurationStatus,
  placeholder,
}: {
  inputRef: RefObject<HTMLTextAreaElement | null>;
  question: string;
  onChange: (question: string) => void;
  onSend: (event: FormEvent<HTMLFormElement>) => void;
  disabled: boolean;
  inputDisabled?: boolean;
  modelLabel: string;
  configurationStatus: string;
  placeholder: string;
}) {
  const composing = useRef(false);
  return (
    <form className="assistant-composer" onSubmit={onSend}>
      <div className="assistant-composer-field">
        <textarea
          ref={inputRef}
          aria-label="你的问题"
          disabled={inputDisabled}
          placeholder={placeholder}
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
      <div className="assistant-composer-meta">
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
