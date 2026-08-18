// westward admin: camera previews + two-rectangle ROI/crop editor.
// Plain JS, no dependencies. CSP-safe (external file, no inline handlers).
(() => {
  

  /* ---------- previews ---------- */
  function wirePreview(scope) {
    (scope || document).querySelectorAll(".preview.pvwrap").forEach((wrap) => {
      var id = wrap.dataset.cam;
      var img = wrap.querySelector("img.pv");
      var empty = wrap.querySelector(".pvempty");
      if (!img || img.dataset.wired) return;
      img.dataset.wired = "1";
      function failed() {
        wrap.classList.add("err");
        if (empty) empty.hidden = false;
      }
      function ok() {
        wrap.classList.remove("err");
        if (empty) empty.hidden = true;
      }
      img.addEventListener("error", failed);
      img.addEventListener("load", ok);
      if (!img.complete || img.naturalWidth === 0) {
        // error may have fired before wiring
        if (img.getAttribute("src")) setTimeout(() => {
          if (!img.complete || img.naturalWidth === 0) failed();
        }, 4000);
      } else ok();

      var btn = wrap.closest(".cam, .card").querySelector("[data-act='preview'][data-cam='" + id + "']");
      if (btn) {
        btn.addEventListener("click", () => {
          btn.disabled = true;
          ok();
          img.src = "/admin/cameras/shot/" + encodeURIComponent(id) + "?v=" + Date.now();
          setTimeout(() => { btn.disabled = false; }, 1500);
        });
      }
    });
  }

  /* ---------- ROI / crop editor ---------- */
  function parseRect(s) {
    if (!s) return null;
    try {
      var a = JSON.parse(s);
      return a && a.length === 4 && a.every(isFinite) ? a : null;
    } catch (e) {
      return null;
    }
  }
  function fmt(r) {
    return r ? r.map((v) => (+v).toFixed(2)).join(", ") : "not set";
  }

  function wireEditor(cv) {
    var id = cv.dataset.cam;
    var card = cv.closest(".cam, .card") || document;
    var roiField = document.getElementById("roi-" + id);
    var cropField = document.getElementById("crop-" + id);
    var readout = card.querySelector(".roi-readout[data-cam='" + id + "']");
    var mode = "roi";
    var roi = parseRect(roiField ? roiField.value : null);
    var crop = parseRect(cropField ? cropField.value : null);
    var drag = null;

    function draw() {
      var ctx = cv.getContext("2d");
      ctx.clearRect(0, 0, cv.width, cv.height);
      function rect(r, color) {
        if (!r) return;
        var x = r[0] * cv.width, y = r[1] * cv.height;
        var w = r[2] * cv.width, h = r[3] * cv.height;
        ctx.shadowColor = "rgba(0,0,0,0.55)";
        ctx.shadowBlur = 6;
        ctx.strokeStyle = color;
        ctx.lineWidth = 3;
        ctx.setLineDash([9, 6]);
        ctx.strokeRect(x, y, w, h);
        ctx.setLineDash([]);
        ctx.shadowBlur = 0;
        ctx.fillStyle = color.replace("1)", "0.10)");
        ctx.fillRect(x, y, w, h);
      }
      rect(roi, "rgba(125,184,255,1)");
      rect(crop, "rgba(245,184,113,1)");

      var aspect = crop ? (crop[2] / crop[3]).toFixed(3) : "-";
      if (readout) {
        while (readout.firstChild) readout.removeChild(readout.firstChild);
        ["scoring ROI ", fmt(roi), " · publish crop ", fmt(crop), " · crop aspect ", aspect]
          .forEach(function (part, i) {
            if (i % 2 === 1) {
              var b = document.createElement("b");
              b.textContent = part;
              readout.appendChild(b);
            } else {
              readout.appendChild(document.createTextNode(part));
            }
          });
      }
    }

    function set(which, r) {
      if (which === "roi") { roi = r; if (roiField) roiField.value = r ? JSON.stringify(r) : ""; }
      else { crop = r; if (cropField) cropField.value = r ? JSON.stringify(r) : ""; }
      draw();
    }

    // mode segmented control
    card.querySelectorAll(".seg button[data-cam='" + id + "']").forEach((b) => {
      if (b.dataset.mode && b.dataset.mode !== "preview") {
        b.addEventListener("click", () => {
          mode = b.dataset.mode;
          card.querySelectorAll(".seg button[data-cam='" + id + "']").forEach((o) => {
            o.classList.toggle("on", o === b);
          });
        });
      }
    });

    function pos(e) {
      var b = cv.getBoundingClientRect();
      return [
        Math.min(1, Math.max(0, (e.clientX - b.left) / b.width)),
        Math.min(1, Math.max(0, (e.clientY - b.top) / b.height)),
      ];
    }
    cv.addEventListener("pointerdown", (e) => {
      var p = pos(e);
      drag = { x0: p[0], y0: p[1] };
      cv.setPointerCapture(e.pointerId);
      e.preventDefault();
    });
    cv.addEventListener("pointermove", (e) => {
      if (!drag) return;
      var p = pos(e);
      set(mode, [
        Math.min(drag.x0, p[0]), Math.min(drag.y0, p[1]),
        Math.abs(p[0] - drag.x0), Math.abs(p[1] - drag.y0),
      ]);
    });
    cv.addEventListener("pointerup", () => {
      if (drag) {
        var v = parseRect(mode === "roi" ? (roiField ? roiField.value : "") : (cropField ? cropField.value : ""));
        if (v) {
          v[2] = Math.min(v[2], 1 - v[0]);
          v[3] = Math.min(v[3], 1 - v[1]);
          if (v[2] < 0.02 || v[3] < 0.02) v = null; // accidental micro-drag clears nothing
          set(mode, v);
        }
      }
      drag = null;
    });

    // clear / copy buttons
    card.querySelectorAll("[data-act='clear'][data-cam='" + id + "']").forEach((b) => {
      b.addEventListener("click", () => { set(b.dataset.mode, null); });
    });
    var copyBtn = card.querySelector("[data-act='copy'][data-cam='" + id + "']");
    if (copyBtn) {
      copyBtn.addEventListener("click", () => {
        if (roi) set("crop", roi.slice());
      });
    }
    draw();
  }

  document.querySelectorAll("canvas.roi").forEach(wireEditor);
  wirePreview(document);

  /* ---------- add-camera form: type swap ---------- */
  var typeSel = document.getElementById("new-type");
  if (typeSel) {
    typeSel.addEventListener("change", () => {
      var ny = typeSel.value === "nyctmc";
      var lbl = document.getElementById("new-ref-label");
      var ref = document.getElementById("new-ref");
      if (lbl) lbl.textContent = ny ? "DOT camera id" : "JPEG URL";
      if (ref) ref.placeholder = ny ? "8a6bc417-…" : "http://…/snapshot.jpg";
    });
  }

  /* ---------- nyctmc publish warning ---------- */
  document.querySelectorAll("input[data-warn-nyctmc]").forEach((chk) => {
    chk.addEventListener("change", () => {
      if (chk.checked && chk.dataset.warnNyctmc === "1") {
        if (!window.confirm("Publishing NYCTMC frames publicly assumes your signed DOT data-sharing agreement covers republication. Proceed?")) {
          chk.checked = false;
        }
      }
    });
  });
})();
