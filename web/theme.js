// Basecoat keys dark mode off html.dark rather than prefers-color-scheme, so
// the media query has to be wired up by hand. Loaded before the body renders
// so the page never flashes light before switching.
(() => {
  const m = matchMedia('(prefers-color-scheme: dark)');
  const apply = () => document.documentElement.classList.toggle('dark', m.matches);
  apply();
  m.addEventListener('change', apply);
})();
