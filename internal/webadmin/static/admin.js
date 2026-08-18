// westward admin: preview fetching + dual-rect ROI/crop editor.
(() => {
  // CSRF: read from the form we will submit (injected server-side).
  function csrf() {
    var el = document.querySelector('input[name="csrf"]');
    return el ? el.value : "";
  }

  // Camera preview: POST id -> image blob (needs multipart-less form body).
  document.addEventListener("click", (e) => {
    var btn = e.target.closest("[data-act='preview']");
    if (!btn) return;
    var id = btn.dataset.cam;
    var img = document.querySelector("img.pv[data-cam='" + id + "']");
    if (!img) return;
    btn.disabled = true;
    fetch("/admin/cameras/preview", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body:
        "csrf=" + encodeURIComponent(csrf()) + "&id=" + encodeURIComponent(id),
    })
      .then((r) => {
        if (!r.ok) throw new Error("status " + r.status);
        return r.blob();
      })
      .then((b) => {
        img.src = URL.createObjectURL(b);
      })
      .catch((err) => {
        alert("preview failed: " + err.message);
      })
      .finally(() => {
        btn.disabled = false;
      });
  });

  // ROI/crop editor: one canvas per camera, two rectangles.
  function parseRect(s) {
    if (!s) return null;
    try {
      var a = JSON.parse(s);
      return a.length === 4 ? a : null;
    } catch (e) {
      return null;
    }
  }
  function fmt(r) {
    return r ? r.map((v) => (+v).toFixed(2)).join(", ") : "none";
  }

  document.querySelectorAll("canvas.roi").forEach((cv) => {
    var id = cv.dataset.cam;
    var roiField = document.getElementById("roi-" + id);
    var cropField = document.getElementById("crop-" + id);
    var readout = document.querySelector(".roi-readout[data-cam='" + id + "']");
    var roi = parseRect(roiField.value);
    var crop = parseRect(cropField.value);
    var mode = "roi"; // which rect the next drag edits

    // mode toggler: shift = crop, plain = roi (plus buttons below)
    var drag = null;

    function draw() {
      var ctx = cv.getContext("2d");
      ctx.clearRect(0, 0, cv.width, cv.height);

      function rect(r, color) {
        if (!r) return;
        ctx.strokeStyle = color;
        ctx.lineWidth = 2;
        ctx.setLineDash([6, 4]);
        ctx.strokeRect(
          r[0] * cv.width,
          r[1] * cv.height,
          r[2] * cv.width,
          r[3] * cv.height,
        );
        ctx.setLineDash([]);
      }
      rect(roi, "#4da3ff");
      rect(crop, "#ffb04d");
      var aspect = crop ? (crop[2] / crop[3]).toFixed(3) : "none";
      readout.textContent =
        "ROI " + fmt(roi) + " · crop " + fmt(crop) + " · crop aspect " + aspect;
    }

    function pos(e) {
      var b = cv.getBoundingClientRect();
      var x = Math.min(1, Math.max(0, (e.clientX - b.left) / b.width));
      var y = Math.min(1, Math.max(0, (e.clientY - b.top) / b.height));
      return [x, y];
    }

    function set(which, r) {
      if (which === "roi") {
        roi = r;
        roiField.value = r ? JSON.stringify(r) : "";
      } else {
        crop = r;
        cropField.value = r ? JSON.stringify(r) : "";
      }
      draw();
    }

    cv.addEventListener("pointerdown", (e) => {
      mode = e.shiftKey ? "crop" : "roi";
      var p = pos(e);
      drag = { x0: p[0], y0: p[1] };
      cv.setPointerCapture(e.pointerId);
    });
    cv.addEventListener("pointermove", (e) => {
      if (!drag) return;
      var p = pos(e);
      var r = [
        Math.min(drag.x0, p[0]),
        Math.min(drag.y0, p[1]),
        Math.abs(p[0] - drag.x0),
        Math.abs(p[1] - drag.y0),
      ];
      set(mode, r);
    });
    cv.addEventListener("pointerup", () => {
      // clamp x+w<=1, y+h<=1
      ["roi", "crop"].forEach((m) => {
        var v = parseRect(m === "roi" ? roiField.value : cropField.value);
        if (v) {
          v[2] = Math.min(v[2], 1 - v[0]);
          v[3] = Math.min(v[3], 1 - v[1]);
          set(m, v);
        }
      });
      drag = null;
    });

    var wrap = cv.closest(".preview");
    wrap.addEventListener("click", (e) => {
      var b = e.target.closest("[data-act]");
      if (!b) return;
      var act = b.dataset.act;
      if (act === "match" && roi) set("crop", roi.slice());
      if (act === "clear-roi") set("roi", null);
      if (act === "clear-crop") set("crop", null);
    });
    draw();
  });

  // new-camera form: swap ref label by type
  var typeSel = document.getElementById("new-type");
  if (typeSel) {
    typeSel.addEventListener("change", () => {
      var ny = typeSel.value === "nyctmc";
      document.getElementById("new-ref-label").textContent = ny
        ? "DOT camera id"
        : "JPEG URL";
      document.getElementById("new-ref").placeholder = ny
        ? "8a6bc417-…"
        : "http://…/snapshot.jpg";
    });
  }

  // nyctmc publish warning
  document.querySelectorAll("input[data-warn-nyctmc]").forEach((chk) => {
    chk.addEventListener("change", () => {
      if (chk.checked && chk.dataset.warnNyctmc === "1") {
        if (
          !confirm(
            "Publishing NYCTMC frames publicly assumes your signed DOT data-sharing agreement covers republication. Proceed?",
          )
        ) {
          chk.checked = false;
        }
      }
    });
  });
})();
