// Live updates and small extras. Everything also works without JavaScript.
(() => {
  if (!window.EventSource || !document.getElementById("board")) return;

  let timer = null;
  const refresh = () => {
    clearTimeout(timer);
    // Several votes at once only need one refresh
    timer = setTimeout(async () => {
      const board = document.getElementById("board");
      if (!board) return;
      try {
        const res = await fetch("/board" + window.location.search, {
          headers: { Accept: "text/html" },
        });
        if (!res.ok) return;
        const html = await res.text();
        const next = new DOMParser()
          .parseFromString(html, "text/html")
          .getElementById("board");
        if (next) board.replaceWith(next);
      } catch {
        // Offline or the server is down, EventSource reconnects by itself
      }
    }, 150);
  };

  const source = new EventSource("/events");
  source.addEventListener("message", refresh);

  let seedOpen = false;
  source.addEventListener("open", () => {
    // The first open is the initial connection. Later ones are reconnects, which
    // may have missed events.
    if (seedOpen) refresh();
    seedOpen = true;
  });
})();

// Keep the scroll position when voting. Voting reorders the board and the
// #film-N anchor would follow the card, so put the reader back where they were.
(() => {
  const KEY = "watchpickr:scroll";
  const here = () => location.pathname + location.search;

  document.addEventListener("submit", (ev) => {
    // Only the board's forms
    if (!ev.target.closest || !ev.target.closest("#board")) return;
    try {
      sessionStorage.setItem(
        KEY,
        JSON.stringify({ url: here(), y: window.scrollY }),
      );
    } catch {
      // Private mode etc, the anchor still works
    }
  });

  window.addEventListener("pageshow", () => {
    let saved = null;
    try {
      saved = JSON.parse(sessionStorage.getItem(KEY) || "null");
      sessionStorage.removeItem(KEY);
    } catch {
      return;
    }
    // Only on the same page, a filter change should start at the top
    if (!saved || saved.url !== here()) return;

    // After the browser's own jump to the anchor. If it ever flickers, nest a
    // second requestAnimationFrame.
    requestAnimationFrame(() => window.scrollTo(0, saved.y));
  });
})();

// Success messages go away after a few seconds, errors stay until dismissed.
(() => {
  const flash = document.querySelector(".flash");
  if (!flash) return;

  flash.title = "Dismiss";
  flash.addEventListener("click", () => flash.remove());

  if (flash.classList.contains("flash-ok")) {
    setTimeout(() => flash.remove(), 4000);
  }
})();

// The dialogs. Without JS a link to #suggest or #filters opens one through
// :target, this makes them real modals.
(() => {
  for (const sheet of document.querySelectorAll("dialog.sheet")) {
    if (typeof sheet.showModal !== "function") return;

    for (const link of document.querySelectorAll(`a[href="#${sheet.id}"]`)) {
      link.addEventListener("click", (ev) => {
        ev.preventDefault();
        sheet.showModal();
      });
    }

    for (const link of sheet.querySelectorAll("a.sheetclose")) {
      link.addEventListener("click", (ev) => {
        ev.preventDefault();
        sheet.close();
      });
    }

    // A click on the dialog itself is a click on the backdrop
    sheet.addEventListener("click", (ev) => {
      if (ev.target === sheet) sheet.close();
    });
  }
})();

// The header menu. <details> works without JS, this adds closing it on an
// outside click or Escape.
(() => {
  const menu = document.querySelector("details.menu");
  if (!menu) return;

  document.addEventListener("click", (ev) => {
    if (menu.open && !menu.contains(ev.target)) menu.open = false;
  });
  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape") menu.open = false;
  });
})();

// Podium live refresh, keyed on the board in the URL since a TV isn't logged in.
(() => {
  const start = document.getElementById("podium");
  if (!start || !window.EventSource) return;

  const code = start.dataset.code || "";
  const q = code ? "?code=" + encodeURIComponent(code) : "";

  let timer = null;
  const refresh = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const here = document.getElementById("podium");
      if (!here) return;
      try {
        const res = await fetch("/podium-top" + q, {
          headers: { Accept: "text/html" },
        });
        if (!res.ok) return;
        const next = new DOMParser()
          .parseFromString(await res.text(), "text/html")
          .getElementById("podium");
        if (next) here.replaceWith(next);
      } catch {
        // Lost the network, EventSource reconnects on its own
      }
    }, 150);
  };

  const source = new EventSource("/podium-live" + q);
  source.addEventListener("message", refresh);

  let seedOpen = false;
  source.addEventListener("open", () => {
    if (seedOpen) refresh();
    seedOpen = true;
  });
})();
