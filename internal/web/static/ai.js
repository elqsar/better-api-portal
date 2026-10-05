// The "Ask AI" menus (templates/_ask_ai.html): open a page in an AI
// assistant with a prompt that links to its Markdown, or copy the prompt
// or the Markdown itself.
(function () {
  function prompt(box) {
    var origin = window.location.origin;
    return "Summarise the following " + box.dataset.what + " document and prepare to answer any other questions.\n\n" +
      "Context: " + origin + box.dataset.md + "\n\n" +
      "Further context on other documents if needed:\n" + origin + "/llms.txt";
  }

  // fill points every "Open in" link under root at its assistant, with the
  // prompt in ?q=.
  function fill(root) {
    root.querySelectorAll(".ask-ai [data-ai-open]").forEach(function (a) {
      a.href = a.dataset.aiOpen + "?q=" + encodeURIComponent(prompt(a.closest(".ask-ai")));
    });
  }

  // copy writes text, or a promise of it, to the clipboard. The promise
  // goes in a ClipboardItem so Safari still counts the click as the user's
  // gesture once the fetch is done.
  function copy(text) {
    if (window.ClipboardItem && navigator.clipboard && navigator.clipboard.write) {
      var blob = Promise.resolve(text).then(function (t) { return new Blob([t], { type: "text/plain" }); });
      return navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);
    }
    return Promise.resolve(text).then(function (t) { return navigator.clipboard.writeText(t); });
  }

  function markdown(box) {
    return fetch(box.dataset.md, { headers: { Accept: "text/markdown" }, credentials: "same-origin" }).then(function (r) {
      if (!r.ok) {
        throw new Error("HTTP " + r.status);
      }
      return r.text();
    });
  }

  function flash(button, label) {
    if (!button.dataset.label) {
      button.dataset.label = button.textContent;
    }
    button.textContent = label;
    clearTimeout(button.flashTimer);
    button.flashTimer = setTimeout(function () { button.textContent = button.dataset.label; }, 1500);
  }

  function closeMenus(except) {
    document.querySelectorAll(".ask-ai details[open]").forEach(function (d) {
      if (d !== except) {
        d.open = false;
      }
    });
  }

  document.addEventListener("click", function (e) {
    var button = e.target.closest(".ask-ai [data-ai-copy]");
    if (button) {
      var box = button.closest(".ask-ai");
      var text = button.dataset.aiCopy === "prompt" ? prompt(box) : markdown(box);
      copy(text).then(function () { flash(button, "Copied"); }, function () { flash(button, "Couldn't copy"); });
      return;
    }
    // A click outside a menu closes it; one on a link inside it too.
    var menu = e.target.closest(".ask-ai details");
    closeMenus(e.target.closest("a") ? null : menu);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      closeMenus(null);
    }
  });

  fill(document);
  // htmx swaps in tabs with their own menus.
  document.addEventListener("htmx:load", function (e) { fill(e.target); });
})();
