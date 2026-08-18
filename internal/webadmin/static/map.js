// westward admin map: cameras + cached DOT cameras on Leaflet.
// External file: CSP forbids inline scripts on /admin.
(() => {
  var el = document.getElementById("map");
  if (!el || typeof L === "undefined") return;

  var home = [
    parseFloat(el.dataset.lat || "40.6782"),
    parseFloat(el.dataset.lon || "-73.9442"),
  ];
  var map = L.map("map").setView(home, 12);

  // Dark basemap matching the dusk theme (CARTO darkMatter over OSM data).
  L.tileLayer("https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png", {
    maxZoom: 19,
    subdomains: "abcd",
    attribution: "&copy; OpenStreetMap contributors &copy; CARTO",
  }).addTo(map);

  // DOT cameras sit on their own layer, shown only when zoomed in enough
  // to actually pick one (at city level 970 dots is visual noise).
  var dotLayer = L.layerGroup();
  var hint = document.querySelector(".mapbar .hint");

  function hintUpdate() {
    if (!hint) return;
    var on = map.getZoom() >= 14;
    hint.textContent = on
      ? "Gold markers are your cameras · dim dots are DOT cameras, click one to add it"
      : "Gold markers are your cameras · zoom in to browse DOT cameras";
  }
  function dotUpdate() {
    if (map.getZoom() >= 14) map.addLayer(dotLayer);
    else map.removeLayer(dotLayer);
  }
  map.on("zoomend", () => {
    dotUpdate();
    hintUpdate();
  });

  function addCamera(fields) {
    var f = document.createElement("form");
    f.method = "post";
    f.action = "/admin/cameras/save";
    f.style.display = "none";
    fields.forEach((kv) => {
      var i = document.createElement("input");
      i.type = "hidden";
      i.name = kv[0];
      i.value = kv[1];
      f.appendChild(i);
    });
    document.body.appendChild(f);
    f.submit();
  }

  fetch("/admin/map/data")
    .then((r) => r.json())
    .then((d) => {
      if (d.home) map.setView([d.home.lat, d.home.lon], 12);

      L.circleMarker([d.home.lat, d.home.lon], {
        radius: 7,
        color: "#f5b871",
        weight: 2,
        fillOpacity: 0.9,
        fillColor: "#e8875f",
      })
        .addTo(map)
        .bindTooltip("watch location");

      (d.cameras || []).forEach((c) => {
        if (c.Lat == null || c.Lon == null) return;
        L.circleMarker([c.Lat, c.Lon], {
          radius: 8,
          color: "#ffd9a8",
          weight: 2,
          fillOpacity: 0.95,
          fillColor: "#ff9d5c",
        })
          .addTo(map)
          .bindTooltip(c.Name + " (" + c.Type + ")")
          .on("click", () => {
            window.location = "/admin/cameras";
          });
      });

      (d.dot || []).forEach((c) => {
        var m = L.circleMarker([c.Lat, c.Lon], {
          radius: 3.5,
          color: "rgba(240,190,160,0.6)",
          weight: 1,
          fillOpacity: c.Online ? 0.7 : 0.2,
          fillColor: "#c9a08a",
        }).addTo(dotLayer);
        m.bindTooltip(
          c.Name + (c.Online ? " · click to add" : " (offline) · click to add"),
        );
        m.on("click", () => {
          if (window.confirm("Add DOT camera " + c.Name + "?")) {
            addCamera([
              ["csrf", document.querySelector('input[name="csrf"]').value],
              ["id", ""],
              ["name", c.Name],
              ["type", "nyctmc"],
              ["ref", c.DotID],
              ["role", "trigger_only"],
              ["lat", String(c.Lat)],
              ["lon", String(c.Lon)],
              ["enabled", "on"],
              ["publish_eligible", "on"],
              ["threshold_abs", "12"],
            ]);
          }
        });
      });

      dotUpdate();
      hintUpdate();
    });
})();
