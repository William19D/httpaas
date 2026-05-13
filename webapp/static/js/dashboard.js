// dashboard.js — refresca el estado VBox de cada tarjeta en vivo.
(function () {
  const list = document.getElementById('instance-list');
  if (!list) return;

  const labels = {
    running: 'ONLINE',
    starting: 'INICIANDO',
    restoring: 'RESTAURANDO',
    saving: 'GUARDANDO',
    stopping: 'DETENIENDO',
    poweroff: 'APAGADA',
    saved: 'SUSPENDIDA',
    paused: 'PAUSADA',
    aborted: 'ABORTADA',
    gurumeditation: 'ABORTADA',
  };
  const klass = {
    running: 'state-running',
    starting: 'state-provisioning',
    restoring: 'state-provisioning',
    saving: 'state-provisioning',
    stopping: 'state-provisioning',
    poweroff: 'state-stopped',
    saved: 'state-stopped',
    paused: 'state-stopped',
    aborted: 'state-failed',
    gurumeditation: 'state-failed',
  };

  function fmt(state) {
    return labels[state] || (state || '').toUpperCase() || '—';
  }
  function cls(state) {
    return klass[state] || 'state-stopped';
  }

  async function refresh() {
    try {
      const res = await fetch('/api/instances');
      if (!res.ok) return;
      const data = await res.json();
      const byId = {};
      for (const inst of data) byId[inst.id] = inst;

      list.querySelectorAll('.instance-card').forEach(card => {
        const id = card.dataset.id;
        const inst = byId[id];
        if (!inst) return;
        const pill = card.querySelector('.state-pill');
        if (pill && pill.dataset.state !== inst.vbox_state) {
          pill.className = 'state-pill ' + cls(inst.vbox_state);
          pill.dataset.state = inst.vbox_state;
          pill.innerHTML = '<span class="state-square"></span>' + fmt(inst.vbox_state);
        }
        const ipInput = card.querySelectorAll('.ic-input code')[2];
        if (ipInput) ipInput.textContent = inst.ip || '—';
      });
    } catch (e) { /* silencioso */ }
  }

  refresh();
  setInterval(refresh, 3000);
})();
