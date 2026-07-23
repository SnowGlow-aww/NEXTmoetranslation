"use client";

import { useEffect, useState } from "react";
import {
  getToken, fetchMe, clearSession, getSetupStatus, getSessionExpiresAt,
  refreshSession, subscribeSessionChanged,
} from "@/lib/api";
import { LoginPage } from "@/components/LoginPage";
import { RegisterPage } from "@/components/RegisterPage";
import { Console } from "@/components/Console";

export default function Home() {
  const [loggedIn, setLoggedIn] = useState<boolean | null>(null);
  const [needsSetup, setNeedsSetup] = useState(false);

  useEffect(() => {
    const dispatchedToken = getToken();
    if (!dispatchedToken) {
      // No session: ask the backend whether this is a fresh install needing
      // first-run admin registration, then show the right page.
      getSetupStatus()
        .then((s) => setNeedsSetup(s.needsSetup))
        .catch(() => setNeedsSetup(false))
        .finally(() => setLoggedIn(false));
      return;
    }
    // Validate the stored token against the backend.
    fetchMe()
      .then(() => setLoggedIn(Boolean(getToken())))
      .catch(() => {
        if (clearSession(dispatchedToken)) setLoggedIn(false);
        else setLoggedIn(Boolean(getToken()));
      });
  }, []);

  useEffect(() => {
    if (!loggedIn) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const expire = (expectedToken: string | null) => {
      if (clearSession(expectedToken)) setLoggedIn(false);
    };
    const schedule = () => {
      if (timer) clearTimeout(timer);
      const scheduledToken = getToken();
      const expiresAt = getSessionExpiresAt();
      if (expiresAt > 0 && expiresAt * 1000 <= Date.now()) {
        expire(scheduledToken);
        return;
      }
      const delay = expiresAt > 0 ? Math.max(1000, expiresAt * 1000 - Date.now() - 60_000) : 1000;
      timer = setTimeout(async () => {
        try {
          await refreshSession();
          if (!stopped) schedule();
        } catch {
          if (stopped) return;
          if (getSessionExpiresAt() * 1000 <= Date.now()) expire(scheduledToken);
          else timer = setTimeout(schedule, 15_000);
        }
      }, delay);
    };
    const unsubscribe = subscribeSessionChanged(schedule);
    schedule();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      unsubscribe();
    };
  }, [loggedIn]);

  if (loggedIn === null) {
    return (
      <div className="center-state" style={{ height: "100vh" }}>
        <div className="spinner" />
        验证身份中…
      </div>
    );
  }
  if (!loggedIn) {
    if (needsSetup) {
      return <RegisterPage onRegister={() => { setNeedsSetup(false); setLoggedIn(true); }} />;
    }
    return <LoginPage onLogin={() => setLoggedIn(true)} />;
  }
  return <Console onLogout={() => setLoggedIn(false)} />;
}
