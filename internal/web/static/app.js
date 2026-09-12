document.addEventListener("DOMContentLoaded", () => {
  // 1. Copy to clipboard
  document.querySelectorAll(".copy-btn").forEach(btn => {
    btn.addEventListener("click", () => {
      const targetId = btn.getAttribute("data-target");
      let text = "";
      if (targetId) {
        const el = document.getElementById(targetId);
        text = el.innerText || el.value;
      } else {
        text = btn.getAttribute("data-clipboard-text");
      }
      if (!text) return;

      navigator.clipboard.writeText(text.trim()).then(() => {
        const original = btn.innerHTML;
        btn.innerHTML = `<span style="color: #10b981; font-size: 0.75rem; font-weight: 600;">✓ Copied!</span>`;
        setTimeout(() => { btn.innerHTML = original; }, 2000);
      });
    });
  });

  // 2. Tab Navigation
  document.querySelectorAll(".tab-btn").forEach(btn => {
    btn.addEventListener("click", () => {
      const tabId = btn.getAttribute("data-tab");
      document.querySelectorAll(".tab-btn").forEach(b => b.classList.remove("active"));
      document.querySelectorAll(".tab-content").forEach(c => c.classList.remove("active"));
      btn.classList.add("active");
      const target = document.getElementById(tabId);
      if (target) target.classList.add("active");
    });
  });

  // 3. Modal controls
  window.openModal = function(id) {
    const el = document.getElementById(id);
    if (el) el.classList.add("open");
  };
  window.closeModal = function(id) {
    const el = document.getElementById(id);
    if (el) el.classList.remove("open");
  };
  document.querySelectorAll(".modal-backdrop").forEach(backdrop => {
    backdrop.addEventListener("click", (e) => {
      if (e.target === backdrop) backdrop.classList.remove("open");
    });
  });

  // 4. GitHub star count (header)
  const starsEl = document.getElementById("github-stars");
  const starsCountEl = document.getElementById("github-stars-count");
  if (starsEl && starsCountEl) {
    fetch("/api/github-stars")
      .then(res => res.ok ? res.json() : Promise.reject())
      .then(data => {
        if (typeof data.stars === "number" && data.stars > 0) {
          starsCountEl.textContent = data.stars.toLocaleString();
          starsEl.hidden = false;
        }
      })
      .catch(() => {});
  }

  // 5. Simulated Terminal Live Log Streamer (on Landing page)
  const logContainer = document.getElementById("terminal-live-logs");
  if (logContainer) {
    const sampleLogs = [
      { method: "GET", path: "/", status: "200 OK", dur: "3.2ms" },
      { method: "GET", path: "/assets/index.js", status: "200 OK", dur: "1.1ms" },
      { method: "GET", path: "/api/health", status: "200 OK", dur: "4.8ms" },
      { method: "POST", path: "/webhooks/stripe", status: "200 OK", dur: "12.4ms" },
      { method: "GET", path: "/ws/live", status: "101 Switching Protocols", dur: "0.8ms" },
      { method: "POST", path: "/api/checkout", status: "201 Created", dur: "24.1ms" }
    ];
    let i = 0;
    setInterval(() => {
      const log = sampleLogs[i % sampleLogs.length];
      i++;
      const line = document.createElement("div");
      line.className = "t-line";
      line.innerHTML = `<span class="t-muted">${new Date().toLocaleTimeString()}</span> <span class="t-success">${log.status.split(" ")[0]}</span> <span class="t-cmd">${log.method}</span> <span class="t-info">${log.path}</span> <span class="t-muted">(${log.dur})</span>`;
      logContainer.appendChild(line);
      if (logContainer.children.length > 5) {
        logContainer.removeChild(logContainer.firstChild);
      }
    }, 2800);
  }
});

