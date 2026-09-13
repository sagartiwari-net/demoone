(function () {
  "use strict";

  function cleanUrl(url) {
    var nextUrl = new URL(url);
    nextUrl.searchParams.delete("accountId");
    return nextUrl.toString();
  }

  function clearActiveToolswalaTab() {
    chrome.tabs.query({ active: true, currentWindow: true }, function (tabs) {
      var tab = tabs && tabs[0];
      if (!tab || !tab.id || !tab.url) return;

      var parsedUrl;
      try {
        parsedUrl = new URL(tab.url);
      } catch (error) {
        return;
      }

      if (!/(^|\.)(?:127\.0\.0\.1|localhost|__H10_PROXY_HOST_ESC__)$/i.test(parsedUrl.hostname)) return;

      var nextUrl = cleanUrl(tab.url);
      var didFinish = false;

      function finish() {
        if (didFinish) return;
        didFinish = true;
        chrome.tabs.update(tab.id, { url: nextUrl }, function () {
          window.close();
        });
      }

      if (chrome.scripting && chrome.scripting.executeScript) {
        chrome.scripting.executeScript(
          {
            target: { tabId: tab.id },
            func: function () {
              window.localStorage.clear();
            }
          },
          finish
        );
        return;
      }

      chrome.tabs.sendMessage(tab.id, { type: "TOOLS_WALA_CLEAR_REFRESH" }, finish);
      setTimeout(finish, 500);
    });
  }

  function createButton() {
    if (document.getElementById("toolswala-popup-clear-refresh")) return;

    var button = document.createElement("button");
    button.id = "toolswala-popup-clear-refresh";
    button.type = "button";
    button.textContent = "Clear & Refresh";
    button.title = "Clear localStorage and reopen this page without accountId";
    button.addEventListener("click", clearActiveToolswalaTab);

    Object.assign(button.style, {
      position: "fixed",
      top: "10px",
      left: "50%",
      transform: "translateX(-50%)",
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

    document.body.appendChild(button);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", createButton, { once: true });
  } else {
    createButton();
  }
})();
