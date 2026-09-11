import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Routes, Route } from "react-router-dom";
import { SessionProvider } from "./lib/session";
import { Layout } from "./components/Layout";
import { ErrorBoundary } from "./components/Common";
import { Auth, Landing } from "./pages/Auth";
import { Dashboard } from "./pages/Dashboard";
import { Subscriptions } from "./pages/Subscriptions";
import { Papers } from "./pages/Papers";
import { Settings } from "./pages/Settings";
import "./style.css";
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      <SessionProvider>
        <BrowserRouter>
          <Routes>
            <Route path="/" element={<Landing />} />
            <Route path="/login" element={<Auth key="login" />} />
            <Route
              path="/register"
              element={<Auth key="register" register />}
            />
            <Route element={<Layout />}>
              <Route path="/app" element={<Dashboard />} />
              <Route path="/subscriptions" element={<Subscriptions />} />
              <Route path="/papers" element={<Papers />} />
              <Route path="/settings" element={<Settings />} />
            </Route>
            <Route path="*" element={<p>页面不存在</p>} />
          </Routes>
        </BrowserRouter>
      </SessionProvider>
    </ErrorBoundary>
  </StrictMode>,
);
