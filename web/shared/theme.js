// Apply the saved theme before first paint so there is no flash. Dark is the
// default; only an explicit choice of light changes it. Loaded blocking from
// <head>, as a file rather than inline so the CSP can forbid inline scripts.
try { document.documentElement.dataset.theme = localStorage.getItem('skybreaker.theme') === 'light' ? 'light' : 'dark' } catch { }
