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
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 18,
    attribution: "&copy; OpenStreetMap contributors",
  }).addTo(map);

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
          radius: 3,
          color: "rgba(210,180,160,0.45)",
          weight: 1,
          fillOpacity: c.Online ? 0.55 : 0.15,
          fillColor: "#c9a08a",
        }).addTo(map);
        m.bindTooltip(c.Name + (c.Online ? "" : " (offline) · click to add"));
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
    });
})();
