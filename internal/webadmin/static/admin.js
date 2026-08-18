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

      function showState(ok, text) {
        wrap.classList.toggle("err", !ok);
        if (empty) {
          empty.hidden = ok;
          while (empty.firstChild) empty.removeChild(empty.firstChild);
          if (!ok) {
            var line1 = document.createElement("strong");
            line1.textContent = "preview unavailable";
            empty.appendChild(line1);
            if (text) {
              var line2 = document.createElement("span");
              line2.textContent = text;
              line2.className = "dim";
              empty.appendChild(line2);
            }
          }
        }
      }

      function load() {
        fetch("/admin/cameras/shot/" + encodeURIComponent(id) + "?v=" + Date.now())
          .then((r) => {
            if (!r.ok) {
              return r.text().then((t) => {
                throw new Error(t.replace(/^(fetch failed: )?/, "") || ("HTTP " + r.status));
              });
            }
            return r.blob();
          })
          .then((b) => {
            img.src = URL.createObjectURL(b);
            showState(true);
          })
          .catch((e) => {
            showState(false, e.message || String(e));
          });
      }

      var btn = wrap
        .closest(".cam, .card")
        .querySelector("[data-act='preview'][data-cam='" + id + "']");
      if (btn) {
        btn.addEventListener("click", () => {
          btn.disabled = true;
          setTimeout(() => {
            btn.disabled = false;
          }, 1500);
          load();
        });
      }
      load();
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
        var x = r[0] * cv.width,
          y = r[1] * cv.height;
        var w = r[2] * cv.width,
          h = r[3] * cv.height;
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
        [
          "scoring ROI ",
          fmt(roi),
          " · publish crop ",
          fmt(crop),
          " · crop aspect ",
          aspect,
        ].forEach(function (part, i) {
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
      if (which === "roi") {
        roi = r;
        if (roiField) roiField.value = r ? JSON.stringify(r) : "";
      } else {
        crop = r;
        if (cropField) cropField.value = r ? JSON.stringify(r) : "";
      }
      draw();
    }

    // mode segmented control
    card.querySelectorAll(".seg button[data-cam='" + id + "']").forEach((b) => {
      if (b.dataset.mode && b.dataset.mode !== "preview") {
        b.addEventListener("click", () => {
          mode = b.dataset.mode;
          card
            .querySelectorAll(".seg button[data-cam='" + id + "']")
            .forEach((o) => {
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
        Math.min(drag.x0, p[0]),
        Math.min(drag.y0, p[1]),
        Math.abs(p[0] - drag.x0),
        Math.abs(p[1] - drag.y0),
      ]);
    });
    cv.addEventListener("pointerup", () => {
      if (drag) {
        var v = parseRect(
          mode === "roi"
            ? roiField
              ? roiField.value
              : ""
            : cropField
              ? cropField.value
              : "",
        );
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
    card
      .querySelectorAll("[data-act='clear'][data-cam='" + id + "']")
      .forEach((b) => {
        b.addEventListener("click", () => {
          set(b.dataset.mode, null);
        });
      });
    var copyBtn = card.querySelector(
      "[data-act='copy'][data-cam='" + id + "']",
    );
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



  /* ---------- universal modal ---------- */
  function buildModal() {
    var d = document.createElement("dialog");
    d.id = "modal";
    var t = document.createElement("h3");
    t.className = "modal-title";
    var m = document.createElement("div");
    m.className = "modal-msg";
    var btns = document.createElement("div");
    btns.className = "modal-btns";
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "btn ghost";
    cancel.dataset.modalCancel = "1";
    cancel.textContent = "Cancel";
    var ok = document.createElement("button");
    ok.type = "button";
    ok.className = "btn";
    ok.dataset.modalOk = "1";
    btns.appendChild(cancel);
    btns.appendChild(ok);
    d.appendChild(t);
    d.appendChild(m);
    d.appendChild(btns);
    document.body.appendChild(d);
    return d;
  }

  // modalConfirm(message, {title, danger, confirmLabel}) -> Promise<bool>
  window.modalConfirm = function (message, opts) {
    opts = opts || {};
    var d = document.getElementById("modal") || buildModal();
    var t = d.querySelector(".modal-title");
    t.textContent = opts.title || "Confirm";
    t.hidden = false;
    var m = d.querySelector(".modal-msg");
    m.textContent = message;
    var ok = d.querySelector("[data-modal-ok]");
    ok.textContent = opts.confirmLabel || "Confirm";
    ok.className = "btn" + (opts.danger ? " danger" : "");
    return new Promise(function (resolve) {
      function done(v) {
        d.close();
        resolve(v);
      }
      ok.onclick = function () { done(true); };
      d.querySelector("[data-modal-cancel]").onclick = function () { done(false); };
      d.oncancel = function (e) { e.preventDefault(); done(false); };
      d.onclick = function (e) { if (e.target === d) done(false); };
      d.showModal();
    });
  };

  // Forms declaring data-confirm ask before submitting.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    var q = form.dataset && form.dataset.confirm;
    if (!q || form.dataset.confirmed) return;
    e.preventDefault();
    modalConfirm(q, { title: "Confirm", danger: true, confirmLabel: form.dataset.confirmLabel || "Delete" })
      .then(function (yes) {
        if (yes) {
          form.dataset.confirmed = "1";
          form.submit();
        }
      });
  });

  // DOT camera preview modal (used by the map): live frame + add/close.
  window.modalDotPreview = function (cam, addFn) {
    var d = document.getElementById("modal") || buildModal();
    // repurpose the shared dialog: title, message slot holds the image
    var title = d.querySelector(".modal-title");
    title.hidden = false;
    title.textContent = cam.Name + (cam.Online ? "" : " (offline)");
    var msg = d.querySelector(".modal-msg");

    var img = document.createElement("img");
    img.className = "modal-img";
    img.alt = "live preview";
    var status = document.createElement("div");
    status.className = "modal-status";
    status.textContent = "loading frame…";
    msg.textContent = "";
    msg.appendChild(img);
    msg.appendChild(status);

    fetch("/admin/dot/shot/" + encodeURIComponent(cam.DotID) + "?v=" + Date.now())
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.blob();
      })
      .then(function (b) {
        img.src = URL.createObjectURL(b);
        status.textContent = "";
      })
      .catch(function (e) {
        img.remove();
        status.textContent = "preview failed: " + (e.message || e);
      });

    var ok = d.querySelector("[data-modal-ok]");
    ok.textContent = "Add camera";
    ok.className = "btn";
    ok.onclick = function () {
      d.close();
      addFn([
        ["csrf", document.querySelector('input[name="csrf"]').value],
        ["id", ""],
        ["name", cam.Name],
        ["type", "nyctmc"],
        ["ref", cam.DotID],
        ["role", "trigger_only"],
        ["lat", String(cam.Lat)],
        ["lon", String(cam.Lon)],
        ["enabled", "on"],
        ["publish_eligible", "on"],
        ["threshold_abs", "12"],
      ]);
    };
    d.querySelector("[data-modal-cancel]").onclick = function () { d.close(); };
    d.oncancel = function (e) { e.preventDefault(); d.close(); };
    d.onclick = function (e) { if (e.target === d) d.close(); };
    if (!d.open) d.showModal();
  };

  /* ---------- notifier add form: type switcher ---------- */
  var ntype = document.getElementById("add-type");
  if (ntype) {
    var panels = document.querySelectorAll("#add-notifier [data-for]");
    function ntypeUpdate() {
      panels.forEach((p) => {
        var on = p.dataset.for === ntype.value;
        p.hidden = !on;
        p.querySelectorAll("input, select").forEach((i) => {
          i.disabled = !on;
        });
      });
    }
    ntype.addEventListener("change", ntypeUpdate);
    ntypeUpdate();
  }
  /* ---------- nyctmc publish warning ---------- */
  document.querySelectorAll("input[data-warn-nyctmc]").forEach((chk) => {
    chk.addEventListener("change", () => {
      if (chk.checked && chk.dataset.warnNyctmc === "1") {
        modalConfirm(
          "Publishing NYCTMC frames publicly assumes your signed DOT data-sharing agreement covers republication. Proceed?",
          { title: "Publish DOT frames", confirmLabel: "Publish" },
        ).then(function (yes) {
          if (!yes) chk.checked = false;
        });
      }
    });
  });
})();
