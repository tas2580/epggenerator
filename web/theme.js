'use strict';

(() => {
  const storageKey = 'epggenerator.theme';
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  const validModes = ['system', 'light', 'dark'];
  const names = {system: 'System', light: 'Hell', dark: 'Dunkel'};
  const icons = {system: '◐', light: '☀', dark: '☾'};
  let preference = 'system';
  let button = null;

  try {
    const saved = localStorage.getItem(storageKey);
    if (validModes.includes(saved)) preference = saved;
  } catch (_) { /* The switch also works when browser storage is unavailable. */ }

  function nextMode() {
    // From System, the first click always switches to the opposite appearance.
    const order = system.matches ? ['system', 'light', 'dark'] : ['system', 'dark', 'light'];
    return order[(order.indexOf(preference) + 1) % order.length];
  }

  function apply() {
    const effective = preference === 'system' ? (system.matches ? 'dark' : 'light') : preference;
    document.documentElement.dataset.theme = effective;
    if (button) {
      document.getElementById('theme-icon').textContent = icons[preference];
      document.getElementById('theme-label').textContent = names[preference];
      const description = `Design: ${names[preference]}${preference === 'system' ? ` (${names[effective]})` : ''}. Wechseln zu ${names[nextMode()]}.`;
      button.title = description;
      button.setAttribute('aria-label', description);
    }
  }

  // Runs in the document head so a saved preference is applied before painting.
  apply();
  system.addEventListener('change', apply);
  window.addEventListener('storage', (event) => {
    if (event.key !== storageKey && event.key !== null) return;
    preference = validModes.includes(event.newValue) ? event.newValue : 'system';
    apply();
  });
  document.addEventListener('DOMContentLoaded', () => {
    button = document.getElementById('theme-toggle');
    if (!button) return;
    button.addEventListener('click', () => {
      preference = nextMode();
      try {
        if (preference === 'system') localStorage.removeItem(storageKey);
        else localStorage.setItem(storageKey, preference);
      } catch (_) { /* Keep the selection for the current page. */ }
      apply();
    });
    apply();
  });
})();
