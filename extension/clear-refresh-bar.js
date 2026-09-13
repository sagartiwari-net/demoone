(function () {
  "use strict";

  if (!/(^|\.)(?:127\.0\.0\.1|localhost|__H10_PROXY_HOST_ESC__)$/i.test(window.location.hostname)) return;
  if (window.top !== window) return;
  if (document.getElementById("clear-bar")) return;

  function clearAndRefresh() {
    try {
      window.localStorage.clear();
    } catch (error) {
      console.warn("Unable to clear localStorage", error);
    }

    var nextUrl = new URL(window.location.href);
    nextUrl.searchParams.delete("accountId");
    window.location.replace(nextUrl.toString());
  }

  if (typeof chrome !== "undefined" && chrome.runtime && chrome.runtime.onMessage) {
    chrome.runtime.onMessage.addListener(function (message) {
      if (message && message.type === "TOOLS_WALA_CLEAR_REFRESH") {
        clearAndRefresh();
      }
    });
  }

  function createBar() {
    if (!document.body) return;

    var bar = document.createElement("button");
    bar.id = "clear-bar";
    bar.type = "button";
    bar.textContent = "Clear & Refresh";
    bar.title = "Clear localStorage and reload this page without accountId";
    bar.addEventListener("click", clearAndRefresh);

    Object.assign(bar.style, {
      position: "fixed",
      top: "8px",
      right: "96px",
      zIndex: "2147483647",
      height: "28px",
      padding: "0 12px",
      border: "1px solid #0a6fe8",
      borderRadius: "6px",
      background: "#0a6fe8",
      color: "#fff",
      font: "600 13px Arial, sans-serif",
      lineHeight: "26px",
      cursor: "pointer",
      boxShadow: "0 2px 8px rgba(0, 0, 0, 0.18)"
    });

    document.body.appendChild(bar);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", createBar, { once: true });
  } else {
    createBar();
  }
})();
