import React, { createContext, useContext, useState, useEffect, useCallback } from 'react';
import { verifyKey } from '../api/client';

interface AuthContextValue {
  isAuthenticated: boolean;
  isLoading: boolean;
  login: (key: string) => Promise<void>;
  logout: () => void;
}

// M-6: the raw API key is deliberately NOT exposed through the context value.
// The api/client reads it directly from sessionStorage; exposing it via
// context would let any component (or any XSS payload that reaches one)
// read the admin bearer token without touching sessionStorage.

// M-6: auto-logout after 30 minutes of inactivity. Limits the window during
// which a stolen token remains useful.
const INACTIVITY_TIMEOUT_MS = 30 * 60 * 1000;
const INACTIVITY_CHECK_INTERVAL_MS = 60 * 1000;

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isLoading, setIsLoading] = useState(true);

  const logout = useCallback(() => {
    sessionStorage.removeItem('adminApiKey');
    setIsAuthenticated(false);
  }, []);

  const validate = useCallback(async () => {
    const token = sessionStorage.getItem('adminApiKey');
    if (!token) {
      setIsAuthenticated(false);
      setIsLoading(false);
      return;
    }
    const ok = await verifyKey();
    if (ok) {
      setIsAuthenticated(true);
    } else {
      sessionStorage.removeItem('adminApiKey');
      setIsAuthenticated(false);
    }
    setIsLoading(false);
  }, []);

  useEffect(() => {
    void validate();
  }, [validate]);

  // M-6: inactivity auto-logout. Listens for user activity events; a periodic
  // check clears credentials when no activity has occurred within the window.
  useEffect(() => {
    if (!isAuthenticated) return;

    let lastActivity = Date.now();
    const updateActivity = () => { lastActivity = Date.now(); };
    const activityEvents = ['click', 'keydown', 'mousemove', 'touchstart'] as const;
    activityEvents.forEach((e) => document.addEventListener(e, updateActivity, { passive: true }));

    const interval = setInterval(() => {
      if (Date.now() - lastActivity > INACTIVITY_TIMEOUT_MS) {
        logout();
      }
    }, INACTIVITY_CHECK_INTERVAL_MS);

    return () => {
      activityEvents.forEach((e) => document.removeEventListener(e, updateActivity));
      clearInterval(interval);
    };
  }, [isAuthenticated, logout]);

  const login = async (newKey: string) => {
    // Save the key before verifying so the API client sends it in the
    // Authorization header during the verifyKey call.
    sessionStorage.setItem('adminApiKey', newKey);
    const ok = await verifyKey();
    if (!ok) {
      sessionStorage.removeItem('adminApiKey');
      throw new Error('API Key 无效或服务端不可达');
    }
    setIsAuthenticated(true);
  };

  return (
    <AuthContext.Provider value={{ isAuthenticated, isLoading, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider');
  }
  return ctx;
}
