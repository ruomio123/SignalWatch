import {
  useId,
  useLayoutEffect,
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";

export type WorkspaceView = "primary" | "assistant";
type View = WorkspaceView;
type Layout = {
  version: 1;
  direction: "horizontal" | "vertical";
  primaryFirst: boolean;
  horizontal: number;
  vertical: number;
};
export type WorkspaceConfig = {
  storageKey: string;
  orderKey: "paperFirst" | "primaryFirst";
  primaryLabel: string;
  primaryShortLabel: string;
  assistantLabel: string;
  primaryClassName: string;
  assistantClassName: string;
  gridClassName: string;
  flowWhenClosed?: boolean;
};
const defaults: Layout = {
  version: 1,
  direction: "horizontal",
  primaryFirst: true,
  horizontal: 0.6,
  vertical: 0.5,
};
function readLayout(config: WorkspaceConfig): Layout {
  try {
    const value = JSON.parse(localStorage.getItem(config.storageKey) ?? "null");
    if (
      value?.version === 1 &&
      ["horizontal", "vertical"].includes(value.direction) &&
      typeof value[config.orderKey] === "boolean" &&
      [value.horizontal, value.vertical].every(
        (ratio) =>
          typeof ratio === "number" &&
          Number.isFinite(ratio) &&
          ratio > 0 &&
          ratio < 1,
      )
    )
      return {
        version: 1,
        direction: value.direction,
        primaryFirst: value[config.orderKey],
        horizontal: value.horizontal,
        vertical: value.vertical,
      };
  } catch {
    /* Browser storage may be unavailable. */
  }
  return { ...defaults };
}
function persist(config: WorkspaceConfig, value: Layout) {
  try {
    const { primaryFirst, ...rest } = value;
    localStorage.setItem(
      config.storageKey,
      JSON.stringify({ ...rest, [config.orderKey]: primaryFirst }),
    );
  } catch {
    /* Layout still works in memory. */
  }
}

export function ResizableWorkspace({
  config,
  onClose,
  enabled,
  primary,
  assistant,
  view,
  onViewChange,
  primaryRef,
  assistantRef,
}: {
  config: WorkspaceConfig;
  onClose?: () => void;
  enabled: boolean;
  primary: ReactNode;
  assistant: ReactNode;
  view: View;
  onViewChange: (view: View) => void;
  primaryRef: RefObject<HTMLElement | null>;
  assistantRef: RefObject<HTMLElement | null>;
}) {
  const [layout, setLayout] = useState(() => readLayout(config));
  const current = useRef(layout);
  const frame = useRef<HTMLDivElement>(null);
  const handle = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{
    pointer: number;
    origin: number;
    ratio: number;
    layout: Layout;
    userSelect: string;
    cursor: string;
  } | null>(null);
  const scroll = useRef<{ element: HTMLElement; top: number; left: number }[]>(
    [],
  );
  const id = useId();
  const vertical = layout.direction === "vertical";
  const compact = size.width < 1080 || (vertical && size.height < 552);
  const tabs = enabled && compact;
  const space = Math.max(1, (vertical ? size.height : size.width) - 12);
  const minimum = (vertical ? 180 : 320) / space;
  const maximum = 1 - 360 / space;
  const clamp = (ratio: number) => Math.max(minimum, Math.min(maximum, ratio));
  const ratio = compact
    ? layout[layout.direction]
    : clamp(layout[layout.direction]);
  const first = layout.primaryFirst ? ratio : 1 - ratio;

  function rememberScroll() {
    scroll.current = Array.from(
      frame.current?.querySelectorAll<HTMLElement>(
        "[data-workspace-primary], .assistant-scroll",
      ) ?? [],
    ).map((element) => ({
      element,
      top: element.scrollTop,
      left: element.scrollLeft,
    }));
  }
  function change(next: Layout, save = true) {
    rememberScroll();
    current.current = next;
    setLayout(next);
    if (save) persist(config, next);
  }
  function release(cancel: boolean) {
    const active = drag.current;
    if (!active) return;
    drag.current = null;
    document.body.style.userSelect = active.userSelect;
    document.body.style.cursor = active.cursor;
    if (handle.current?.hasPointerCapture(active.pointer))
      handle.current.releasePointerCapture(active.pointer);
    setDragging(false);
    if (cancel) change(active.layout, false);
    else persist(config, current.current);
  }
  useLayoutEffect(() => {
    const element = frame.current;
    if (!element) return;
    const observer = new ResizeObserver(() => {
      rememberScroll();
      release(true);
      const rect = element.getBoundingClientRect();
      if (
        rect.width < 1080 ||
        (current.current.direction === "vertical" && rect.height < 552)
      ) {
        if (assistantRef.current?.contains(document.activeElement))
          onViewChange("assistant");
        else if (primaryRef.current?.contains(document.activeElement))
          onViewChange("primary");
      }
      setSize({ width: rect.width, height: rect.height });
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  useEffect(
    () => () => {
      const active = drag.current;
      if (active) {
        document.body.style.userSelect = active.userSelect;
        document.body.style.cursor = active.cursor;
        if (handle.current?.hasPointerCapture(active.pointer))
          handle.current.releasePointerCapture(active.pointer);
      }
    },
    [],
  );
  useLayoutEffect(() => {
    if (!enabled || compact) release(true);
    if (tabs) {
      if (assistantRef.current?.contains(document.activeElement))
        onViewChange("assistant");
      else if (primaryRef.current?.contains(document.activeElement))
        onViewChange("primary");
    }
    for (const { element, top, left } of scroll.current) {
      element.scrollTop = top;
      element.scrollLeft = left;
    }
    scroll.current = [];
  }, [layout, size, tabs, enabled]);

  const panels = {
    primary: (
      <section
        key="primary"
        ref={primaryRef}
        id={`${id}-primary`}
        className={config.primaryClassName}
        data-workspace-primary
        role={tabs ? "tabpanel" : undefined}
        aria-label={config.primaryLabel}
        aria-labelledby={tabs ? `${id}-primary-tab` : undefined}
        hidden={tabs && view !== "primary"}
        tabIndex={0}
      >
        {primary}
      </section>
    ),
    assistant: enabled ? (
      <aside
        key="assistant"
        ref={assistantRef}
        id={`${id}-assistant`}
        className={config.assistantClassName}
        role={tabs ? "tabpanel" : undefined}
        aria-label={config.assistantLabel}
        onKeyDown={(event) => {
          if (
            event.key === "Escape" &&
            onClose &&
            !document.querySelector("dialog:modal")
          ) {
            event.preventDefault();
            onClose();
          }
        }}
        aria-labelledby={tabs ? `${id}-assistant-tab` : undefined}
        hidden={tabs && view !== "assistant"}
      >
        {assistant}
      </aside>
    ) : null,
  };
  const separator = (
    <div
      key="separator"
      ref={handle}
      className="split-handle"
      hidden={!enabled || compact}
      role="separator"
      tabIndex={enabled && !compact ? 0 : -1}
      aria-label={`调整${config.primaryShortLabel}面板大小`}
      aria-controls={`${id}-primary ${id}-assistant`}
      aria-orientation={vertical ? "horizontal" : "vertical"}
      aria-valuemin={Math.round(minimum * 100)}
      aria-valuemax={Math.round(maximum * 100)}
      aria-valuenow={Math.round(ratio * 100)}
      aria-valuetext={`${config.primaryShortLabel}占 ${Math.round(ratio * 100)}%`}
      title="拖动调整大小；方向键调整，Shift 加速，Home/End 到达边界，双击均分"
      onDoubleClick={() =>
        change({ ...layout, [layout.direction]: clamp(0.5) })
      }
      onPointerDown={(event) => {
        if (!event.isPrimary || event.button !== 0 || compact || drag.current)
          return;
        event.preventDefault();
        event.currentTarget.focus({ preventScroll: true });
        event.currentTarget.setPointerCapture(event.pointerId);
        drag.current = {
          pointer: event.pointerId,
          origin: vertical ? event.clientY : event.clientX,
          ratio,
          layout,
          userSelect: document.body.style.userSelect,
          cursor: document.body.style.cursor,
        };
        document.body.style.userSelect = "none";
        document.body.style.cursor = vertical ? "row-resize" : "col-resize";
        setDragging(true);
      }}
      onPointerMove={(event) => {
        const active = drag.current;
        if (!active || active.pointer !== event.pointerId) return;
        const delta =
          ((vertical ? event.clientY : event.clientX) - active.origin) / space;
        change(
          {
            ...layout,
            [layout.direction]: clamp(
              active.ratio + (layout.primaryFirst ? delta : -delta),
            ),
          },
          false,
        );
      }}
      onPointerUp={(event) => {
        if (drag.current?.pointer === event.pointerId) release(false);
      }}
      onPointerCancel={(event) => {
        if (drag.current?.pointer === event.pointerId) release(true);
      }}
      onLostPointerCapture={(event) => {
        if (drag.current?.pointer === event.pointerId) release(true);
      }}
      onKeyDown={(event) => {
        const backward = vertical ? "ArrowUp" : "ArrowLeft";
        const forward = vertical ? "ArrowDown" : "ArrowRight";
        if (![backward, forward, "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const step =
          (event.shiftKey ? 0.1 : 0.02) * (layout.primaryFirst ? 1 : -1);
        const next =
          event.key === "Home"
            ? minimum
            : event.key === "End"
              ? maximum
              : ratio + (event.key === forward ? step : -step);
        change({ ...layout, [layout.direction]: clamp(next) });
      }}
    >
      <span aria-hidden="true" />
    </div>
  );

  return (
    <div
      className={`split-workspace-container${config.flowWhenClosed && !enabled ? " is-flow" : ""}`}
    >
      {enabled && (
        <div
          className="split-layout-controls"
          role="group"
          aria-label="面板布局"
        >
          <button
            className="button"
            aria-pressed={!vertical}
            onClick={() => change({ ...layout, direction: "horizontal" })}
          >
            左右排列
          </button>
          <button
            className="button"
            aria-pressed={vertical}
            onClick={() => change({ ...layout, direction: "vertical" })}
          >
            上下排列
          </button>
          <button
            className="button"
            onClick={() =>
              change({ ...layout, primaryFirst: !layout.primaryFirst })
            }
          >
            交换位置
          </button>
          <button className="button" onClick={() => change({ ...defaults })}>
            恢复默认
          </button>
        </div>
      )}
      <div ref={frame} className="split-workspace-frame">
        {tabs && (
          <div
            className="split-view-tabs"
            role="tablist"
            aria-label={`${config.primaryShortLabel}视图`}
          >
            {(["primary", "assistant"] as View[]).map((tab) => (
              <button
                key={tab}
                id={`${id}-${tab}-tab`}
                role="tab"
                aria-controls={`${id}-${tab}`}
                aria-selected={view === tab}
                tabIndex={view === tab ? 0 : -1}
                onClick={() => onViewChange(tab)}
                onKeyDown={(event) => {
                  if (
                    !["ArrowLeft", "ArrowRight", "Home", "End"].includes(
                      event.key,
                    )
                  )
                    return;
                  event.preventDefault();
                  const next =
                    event.key === "Home"
                      ? "primary"
                      : event.key === "End"
                        ? "assistant"
                        : view === "primary"
                          ? "assistant"
                          : "primary";
                  onViewChange(next);
                  document.getElementById(`${id}-${next}-tab`)?.focus();
                }}
              >
                {tab === "primary" ? config.primaryLabel : "AI 助手"}
              </button>
            ))}
          </div>
        )}
        <div
          className={`split-workspace ${config.gridClassName}${enabled && !compact ? " is-split" : ""}${dragging ? " is-resizing" : ""}`}
          data-direction={layout.direction}
          style={
            enabled && !compact
              ? {
                  [vertical ? "gridTemplateRows" : "gridTemplateColumns"]:
                    `minmax(0, ${first}fr) 12px minmax(0, ${1 - first}fr)`,
                }
              : undefined
          }
        >
          {layout.primaryFirst
            ? [panels.primary, separator, panels.assistant]
            : [panels.assistant, separator, panels.primary]}
        </div>
      </div>
    </div>
  );
}
