"use client";

import React, { useState } from "react";
import { login, setSession } from "@/lib/api";

export function LoginPage({ onLogin }: { onLogin: () => void }) {
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      const res = await login(user, pass);
      setSession(res);
      onLogin();
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <h1>翻译校对</h1>
        <p className="sub">Moesekai Translation Console</p>
        {error && <div className="login-error" role="alert">{error}</div>}
        <label className="sr-only" htmlFor="login-username">用户名</label>
        <input
          id="login-username"
          type="text"
          placeholder="用户名"
          value={user}
          onChange={(e) => setUser(e.target.value)}
          autoFocus
        />
        <label className="sr-only" htmlFor="login-password">密码</label>
        <input
          id="login-password"
          type="password"
          placeholder="密码"
          value={pass}
          onChange={(e) => setPass(e.target.value)}
        />
        <button className="btn btn-primary" type="submit" disabled={loading || !user || !pass}>
          {loading ? "登录中…" : "登录"}
        </button>
      </form>
    </div>
  );
}
