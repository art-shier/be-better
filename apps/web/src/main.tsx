import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { AuthProvider, useAuth } from "./auth/AuthProvider";
import { AppStoreProvider, type StoreIdentity } from "./store/AppStore";
import { UiProvider } from "./ui/UiProvider";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AuthProvider><IdentityRoot /></AuthProvider>
  </StrictMode>,
);

function IdentityRoot() {
  const auth = useAuth();
  if (auth.mode === "loading") return <div className="app-loading"><span className="brand-mark">序</span><p>正在确认数据空间…</p></div>;
  const identity: StoreIdentity = auth.user ? { kind: "user", userId: auth.user.id } : { kind: "guest" };
  return <AppStoreProvider key={auth.identityKey} identity={identity} syncEnabled={auth.mode === "authenticated"} onUnauthorized={auth.markSessionExpired} onServiceOffline={auth.markServiceOffline} onServiceOnline={auth.markServiceOnline}><UiProvider><App /></UiProvider></AppStoreProvider>;
}

if (import.meta.env.PROD && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js");
  });
}
