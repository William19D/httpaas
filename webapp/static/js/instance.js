// instance.js - polling del estado y logs de la instancia
(function () {
  if (typeof instanceId === 'undefined') return;
  const viewer = document.getElementById('log-viewer');
  const counter = document.getElementById('log-counter');
  let lastCount = viewer ? viewer.children.length : 0;

  function levelClass(lvl) {
    return 'log-line log-' + (lvl || 'info');
  }

  async function poll() {
    try {
      const res = await fetch('/api/instances/' + instanceId + '/status');
      if (!res.ok) return;
      const inst = await res.json();
      if (!inst || !inst.logs) return;

      if (inst.logs.length !== lastCount) {
        lastCount = inst.logs.length;
        if (viewer) {
          viewer.innerHTML = '';
          for (const line of inst.logs) {
            const div = document.createElement('div');
            div.className = levelClass(line.lvl);
            const t = new Date(line.t).toLocaleTimeString();
            div.innerHTML =
              '<span class="log-time">' + t + '</span>' +
              '<span class="log-level">[' + (line.lvl || 'info').toUpperCase() + ']</span>' +
              '<span class="log-msg"></span>';
            div.querySelector('.log-msg').textContent = line.msg;
            viewer.appendChild(div);
          }
          viewer.scrollTop = viewer.scrollHeight;
        }
        if (counter) counter.textContent = inst.logs.length + ' eventos';
      }

      // Si pasó a running/failed, recarga la página para actualizar UI completa.
      const pill = document.querySelector('.state-pill');
      if (pill && pill.dataset.state !== inst.state) {
        if (inst.state === 'running' || inst.state === 'failed') {
          setTimeout(() => location.reload(), 800);
        }
      }
    } catch (e) {}
  }

  poll();
  setInterval(poll, 3000);
})();
