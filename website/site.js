// Site vitrine de FRP Manager : thème, menu mobile, copie des commandes,
// onglets, démo intégrée et sommaire de la documentation. Aucune dépendance.
(function () {
  'use strict';
  const root = document.documentElement;
  const body = document.body;

  // ── Thème : clair / sombre, mémorisé dans ce navigateur ─────────────────
  const themeBtn = document.querySelector('.theme');
  if (themeBtn) {
    themeBtn.addEventListener('click', () => {
      const dark = root.dataset.theme === 'dark'
        || (!root.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches);
      root.dataset.theme = dark ? 'light' : 'dark';
      try { localStorage.setItem('frpm.site.theme', root.dataset.theme); } catch (e) { /* navigation privée */ }
    });
  }

  // ── Menu mobile ─────────────────────────────────────────────────────────
  const toggle = document.querySelector('.nav-toggle');
  const nav = document.getElementById('nav');
  if (toggle && nav) {
    toggle.addEventListener('click', () => {
      const open = nav.classList.toggle('open');
      toggle.setAttribute('aria-expanded', String(open));
    });
    nav.addEventListener('click', (e) => {
      if (e.target.closest('a')) { nav.classList.remove('open'); toggle.setAttribute('aria-expanded', 'false'); }
    });
  }

  // ── Bouton « Copier » sur chaque bloc de code ───────────────────────────
  document.querySelectorAll('.code').forEach((block) => {
    const pre = block.querySelector('pre');
    if (!pre || !navigator.clipboard) return;
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy';
    btn.textContent = body.dataset.copy || 'Copy';
    btn.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(pre.innerText.replace(/\n$/, ''));
        btn.textContent = body.dataset.copied || 'Copied';
        btn.classList.add('done');
        setTimeout(() => { btn.textContent = body.dataset.copy || 'Copy'; btn.classList.remove('done'); }, 1600);
      } catch (e) { /* presse-papiers refusé : l'utilisateur sélectionne à la main */ }
    });
    block.append(btn);
  });

  // ── Onglets (flèches gauche/droite au clavier) ──────────────────────────
  document.querySelectorAll('[data-tabs]').forEach((tabs) => {
    const buttons = [...tabs.querySelectorAll('[role="tab"]')];
    const select = (btn) => {
      buttons.forEach((b) => {
        const on = b === btn;
        b.setAttribute('aria-selected', String(on));
        b.tabIndex = on ? 0 : -1;
        document.getElementById(b.getAttribute('aria-controls')).hidden = !on;
      });
    };
    buttons.forEach((b, i) => {
      b.addEventListener('click', () => select(b));
      b.addEventListener('keydown', (e) => {
        const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
        if (!d) return;
        const next = buttons[(i + d + buttons.length) % buttons.length];
        select(next);
        next.focus();
      });
    });
  });

  // ── Démo : chargée seulement quand on la lance ──────────────────────────
  document.querySelectorAll('.demo-frame').forEach((frame) => {
    const launch = frame.querySelector('.demo-launch');
    if (!launch) return;
    launch.addEventListener('click', () => {
      const iframe = document.createElement('iframe');
      iframe.src = frame.dataset.src;
      iframe.title = launch.textContent.trim();
      launch.replaceWith(iframe);
      iframe.focus();
    });
  });

  // ── Documentation : titre en cours de lecture dans « Sur cette page » ──
  const toc = document.querySelector('.docs-toc');
  if (toc && 'IntersectionObserver' in window) {
    const links = new Map([...toc.querySelectorAll('a')].map((a) => [decodeURIComponent(a.hash.slice(1)), a]));
    const visible = new Set();
    const heads = [...document.querySelectorAll('.prose h2[id], .prose h3[id]')];
    const obs = new IntersectionObserver((entries) => {
      entries.forEach((en) => (en.isIntersecting ? visible.add(en.target) : visible.delete(en.target)));
      const first = heads.find((h) => visible.has(h));
      if (!first) return;
      links.forEach((a) => a.classList.remove('active'));
      const link = links.get(first.id);
      if (link) link.classList.add('active');
    }, { rootMargin: '-70px 0px -65% 0px' });
    heads.forEach((h) => obs.observe(h));
  }
}());
