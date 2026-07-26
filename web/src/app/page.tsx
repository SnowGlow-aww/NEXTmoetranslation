"use client";

import { useCallback, useEffect, useState } from "react";
import {
  getToken, fetchMe, clearSession, ensureSessionMigrated, getSessionEnvelope, getSessionEpoch,
  getSetupStatus, getSessionExpiresAt, refreshSession, subscribeSessionChanged,
} from "@/lib/api";
import { LoginPage } from "@/components/LoginPage";
import { RegisterPage } from "@/components/RegisterPage";
import { Console } from "@/components/Console";

export default function Home() {
  const [loggedIn, setLoggedIn] = useState<boolean | null>(null);
  const [needsSetup, setNeedsSetup] = useState(false);
  const [sessionEpoch, setSessionEpoch] = useState("");
  const [initializationError, setInitializationError] = useState("");

  const initialize = useCallback(async () => {
    setInitializationError("");
    setLoggedIn(null);
    try {
      await ensureSessionMigrated();
      for (let attempt = 0; attempt < 2; attempt++) {
        const dispatched = getSessionEnvelope();
        setSessionEpoch(dispatched?.epoch || "");
        if (!dispatched?.session) {
          // No session: ask the backend whether this is a fresh install needing
          // first-run admin registration, then show the right page.
          const setup = await getSetupStatus().catch(() => ({ needsSetup: false }));
          setNeedsSetup(setup.needsSetup);
          setLoggedIn(false);
          return;
        }
        try {
          await fetchMe();
          setLoggedIn(Boolean(getToken()));
          return;
        } catch (reason) {
          const current = getSessionEnvelope();
          if (current?.session && (current.epoch !== dispatched.epoch || current.session.token !== dispatched.session.token)) {
            continue;
          }
          if (current?.session) {
            setInitializationError(reason instanceof Error ? reason.message : "身份服务暂时不可用");
            return;
          }
          setLoggedIn(false);
          return;
        }
      }
      setInitializationError("会话在身份校验期间持续变化，请重试");
    } catch (reason) {
      if (getSessionEnvelope()?.session) {
        setInitializationError(reason instanceof Error ? reason.message : "身份服务暂时不可用");
      } else {
        setLoggedIn(false);
      }
    }
  }, []);

  useEffect(() => {
    void initialize();
  }, [initialize]);

  useEffect(() => subscribeSessionChanged(() => {
    setSessionEpoch(getSessionEpoch());
    setLoggedIn(Boolean(getToken()));
  }), []);

  useEffect(() => {
    if (!loggedIn) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const expire = async (expected = getSessionEnvelope()) => {
      if (await clearSession(expected)) setLoggedIn(false);
    };
    const schedule = () => {
      if (timer) clearTimeout(timer);
      const scheduledSession = getSessionEnvelope();
      const expiresAt = getSessionExpiresAt();
      if (expiresAt > 0 && expiresAt * 1000 <= Date.now()) {
        void expire(scheduledSession);
        return;
      }
      const delay = expiresAt > 0 ? Math.max(1000, expiresAt * 1000 - Date.now() - 60_000) : 1000;
      timer = setTimeout(async () => {
        try {
          await refreshSession();
          if (!stopped) schedule();
        } catch {
          if (stopped) return;
          if (getSessionExpiresAt() * 1000 <= Date.now()) void expire(scheduledSession);
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
    if (initializationError) {
      return (
        <div className="center-state session-offline" style={{ height: "100vh" }} role="alert">
          <h2>暂时无法验证身份</h2>
          <p>已保留当前共享会话。网络或服务恢复后重试，不会因为临时 5xx 或连接失败而退出。</p>
          <code>{initializationError}</code>
          <button className="btn btn-primary" onClick={() => void initialize()}>重试身份验证</button>
        </div>
      );
    }
    return (
      <div className="center-state" style={{ height: "100vh" }}>
        <div className="spinner" />
        验证身份中…
      </div>
    );
  }
  if (!loggedIn) {
    if (needsSetup) {
      return <RegisterPage onRegister={() => { setNeedsSetup(false); setSessionEpoch(getSessionEpoch()); setLoggedIn(true); }} />;
    }
    return <LoginPage onLogin={() => { setSessionEpoch(getSessionEpoch()); setLoggedIn(true); }} />;
  }
  return <Console key={sessionEpoch} onLogout={() => setLoggedIn(false)} />;
}
