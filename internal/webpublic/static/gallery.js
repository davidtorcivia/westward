// Lightbox: open on click, Esc/backdrop close, arrows navigate within the
// loaded page, focus trap + restore. No dependencies.
(() => {
  
  var lb = document.getElementById("lb");
  if (!lb) return;
  var img = document.getElementById("lb-img");
  var cap = document.getElementById("lb-cap");
  var buttons = Array.prototype.slice.call(document.querySelectorAll(".ph"));
  var idx = -1;
  var lastFocus = null;

  function show(i) {
    if (i < 0 || i >= buttons.length) return;
    idx = i;
    var b = buttons[i];
    img.src = b.dataset.best;
    img.alt = b.querySelector("img").alt;
    var text = b.dataset.date + " · " + b.dataset.score;
    if (b.dataset.attr) text += " · " + b.dataset.attr;
    cap.textContent = text;
  }

  function open(i) {
    lastFocus = document.activeElement;
    show(i);
    lb.hidden = false;
    lb.querySelector(".lb-close").focus();
  }

  function close() {
    lb.hidden = true;
    if (idx >= 0 && buttons[idx]) buttons[idx].focus();
    else if (lastFocus) lastFocus.focus();
    idx = -1;
  }

  buttons.forEach((b, i) => {
    b.addEventListener("click", () => { open(i); });
  });
  lb.querySelector(".lb-close").addEventListener("click", close);
  lb.addEventListener("click", (e) => { if (e.target === lb) close(); });
  document.addEventListener("keydown", (e) => {
    if (lb.hidden) return;
    if (e.key === "Escape") close();
    else if (e.key === "ArrowRight") { show(idx + 1); e.preventDefault(); }
    else if (e.key === "ArrowLeft") { show(idx - 1); e.preventDefault(); }
    else if (e.key === "Tab") {
      // two focusable elements: trap between them
      var f = [lb.querySelector(".lb-close")];
      if (document.activeElement !== f[0]) { f[0].focus(); e.preventDefault(); }
    }
  });
})();
