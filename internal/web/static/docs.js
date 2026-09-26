// Renders the OpenAPI reference in #api-docs with Scalar. It runs offline:
// no web fonts, telemetry, request proxy, AI agent or MCP, and the page's
// CSP blocks every other host anyway, so spec content never leaves.
(function () {
  var el = document.getElementById("api-docs");
  if (!el || !window.Scalar) {
    return;
  }
  var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  window.Scalar.createApiReference(el, {
    url: el.dataset.url,
    withDefaultFonts: false,
    telemetry: false,
    hideClientButton: true,
    hideTestRequestButton: true,
    agent: { disabled: true },
    mcp: { disabled: true },
    showDeveloperTools: "never",
    hideDarkModeToggle: true,
    forceDarkModeState: dark ? "dark" : "light",
    documentDownloadType: "json",
    externalUrls: { dashboardUrl: "", registryUrl: "", proxyUrl: "", apiBaseUrl: "" },
  });
})();
