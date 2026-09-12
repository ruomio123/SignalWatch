import { useEffect, useRef, useState } from "react";
import { NavLink, Outlet, Navigate, useLocation, Link } from "react-router-dom";
import {
  KeyRound,
  ChevronDown,
  FileText,
  House,
  LogOut,
  PanelLeft,
  Rss,
  Settings,
  X,
} from "lucide-react";
import {
  Brand,
  ErrorBoundary,
  ErrorNotice,
  IconButton,
  Button,
  trapDialogFocus,
} from "./Common";
import { useSession } from "../lib/session";
import { AccountProvider, useAccount } from "../lib/account";
const links = [
  { to: "/app", label: "概览", icon: House },
  { to: "/papers", label: "匹配论文", icon: FileText },
  { to: "/subscriptions", label: "订阅", icon: Rss },
  { to: "/settings", label: "偏好设置", icon: Settings },
  { to: "/api-keys", label: "API 管理", icon: KeyRound },
];
export function Layout() {
  const { token } = useSession();
  const location = useLocation();
  if (!token)
    return (
      <Navigate
        to={
          "/login?next=" +
          encodeURIComponent(location.pathname + location.search)
        }
        replace
      />
    );
  return (
    <AccountProvider key={token}>
      <Workspace />
    </AccountProvider>
  );
}
function Navigation({ close }: { close?: () => void }) {
  const { logout, loggingOut } = useSession();
  return (
    <>
      <div className="sidebar-content">
        <Link className="brand" to="/app" onClick={close}>
          <Brand />
        </Link>
        <nav aria-label="主导航">
          {links.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              onClick={close}
              className={({ isActive }) =>
                `sidebar-link ${isActive ? "active" : ""}`
              }
            >
              <Icon size={18} aria-hidden="true" />
              {label}
            </NavLink>
          ))}
        </nav>
      </div>
      <div className="sidebar-bottom">
        <button onClick={logout} disabled={loggingOut}>
          <LogOut size={17} aria-hidden="true" />
          退出登录
        </button>
      </div>
    </>
  );
}
function Workspace() {
  const { logout, logoutError } = useSession();
  const location = useLocation();
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [mobile, setMobile] = useState(
    () => matchMedia("(max-width: 767px)").matches,
  );
  const drawer = useRef<HTMLDialogElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    setMobileOpen(false);
  }, [location.pathname]);
  useEffect(() => {
    const media = matchMedia("(min-width: 768px)");
    const change = () => {
      setMobile(!media.matches);
      if (media.matches) setMobileOpen(false);
    };
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);
  useEffect(() => {
    if (!mobileOpen) return;
    const element = drawer.current;
    element?.showModal();
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      element?.close();
      document.body.style.overflow = previous;
      toggle.current?.focus();
    };
  }, [mobileOpen]);
  return (
    <div className={`app-shell ${collapsed ? "sidebar-collapsed" : ""}`}>
      <a className="skip-link" href="#main-content">
        跳转到主要内容
      </a>
      <aside
        className="sidebar"
        aria-label="工作台侧栏"
        inert={collapsed || mobile}
        aria-hidden={collapsed || mobile}
      >
        <Navigation />
      </aside>
      {mobileOpen && (
        <dialog
          ref={drawer}
          className="mobile-drawer"
          aria-label="导航菜单"
          onKeyDown={trapDialogFocus}
          onCancel={(e) => {
            e.preventDefault();
            setMobileOpen(false);
          }}
          onClick={(e) => {
            if (e.target === e.currentTarget) {
              const box = e.currentTarget.getBoundingClientRect();
              if (e.clientX > box.right) setMobileOpen(false);
            }
          }}
        >
          <IconButton label="关闭导航" onClick={() => setMobileOpen(false)}>
            <X size={18} />
          </IconButton>
          <Navigation close={() => setMobileOpen(false)} />
        </dialog>
      )}
      <div className="workspace">
        <header className="topbar">
          <div className="topbar-start">
            <button
              ref={toggle}
              className="button button-ghost icon-button navigation-toggle"
              aria-label="切换导航"
              aria-expanded={mobile ? mobileOpen : !collapsed}
              onClick={() => {
                if (matchMedia("(max-width: 767px)").matches)
                  setMobileOpen(true);
                else setCollapsed(!collapsed);
              }}
            >
              <PanelLeft size={20} aria-hidden="true" />
            </button>
            <span>{links.find((l) => l.to === location.pathname)?.label}</span>
          </div>
          <AccountMenu />
        </header>
        <main id="main-content" className="workspace-inner" tabIndex={-1}>
          {!!logoutError && <ErrorNotice error={logoutError} retry={logout} />}
          <ErrorBoundary key={location.pathname}>
            <Outlet />
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}
function AccountMenu() {
  const account = useAccount();
  const { logout, loggingOut } = useSession();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const location = useLocation();
  useEffect(() => {
    setOpen(false);
  }, [location.pathname]);
  useEffect(() => {
    if (!open) return;
    const click = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        root.current?.querySelector("button")?.focus();
      }
    };
    document.addEventListener("pointerdown", click);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("pointerdown", click);
      document.removeEventListener("keydown", key);
    };
  }, [open]);
  const email = account.data?.email;
  return (
    <div ref={root} className="account-menu">
      <button
        className="account-trigger"
        aria-label="账户菜单"
        aria-expanded={open}
        aria-controls="account-dropdown"
        onClick={() => setOpen(!open)}
      >
        <span className="avatar">{email?.[0]?.toUpperCase() ?? "S"}</span>
        <span className="account-email">{email ?? "我的账户"}</span>
        <ChevronDown size={16} aria-hidden="true" />
      </button>
      {open && (
        <div className="account-dropdown" id="account-dropdown">
          <p className="muted">
            {email ?? (account.loading ? "正在读取账户…" : "账户信息暂不可用")}
          </p>
          {!!account.error && (
            <Button onClick={account.reload}>重新读取账户</Button>
          )}
          <Link to="/settings" onClick={() => setOpen(false)}>
            <Settings size={16} />
            偏好设置
          </Link>
          <button onClick={logout} disabled={loggingOut}>
            <LogOut size={16} />
            退出登录
          </button>
        </div>
      )}
    </div>
  );
}
