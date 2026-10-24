// Run by Skybreaker in the page, never by the agent. Each function returns
// JSON; none returns what is typed into a field.
(() => {
  const REF = 'data-sky-ref';
  // secret is a field only you type into. A frame counts: what's inside
  // (a card form, a sign-in) can't be told from here.
  const secret = (el) => {
    if (!el) return false;
    if (el.tagName === 'IFRAME' || el.tagName === 'FRAME') return true;
    if (el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') return false;
    const t = (el.getAttribute('type') || '').toLowerCase();
    const ac = (el.getAttribute('autocomplete') || '').toLowerCase();
    const masked = (getComputedStyle(el).webkitTextSecurity || 'none') !== 'none';
    return t === 'password' || masked || /(^|\s)(current-password|new-password|one-time-code)(\s|$)/.test(ac) || /(^|\s)cc-/.test(ac);
  };
  // focusedEl is the element with focus, inside shadow roots too.
  const focusedEl = () => {
    let el = document.activeElement;
    while (el && el.shadowRoot && el.shadowRoot.activeElement) el = el.shadowRoot.activeElement;
    return el;
  };
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
  };
  const label = (el) => {
    const id = el.id && document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
    const parts = [el.getAttribute('aria-label'), id && id.innerText, el.getAttribute('placeholder'), el.getAttribute('title'), el.getAttribute('alt'), el.getAttribute('name')];
    if (!/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) parts.unshift(el.innerText);
    for (const p of parts) if (p && p.trim()) return p.trim().replace(/\s+/g, ' ').slice(0, 120);
    return '';
  };
  const byRef = (ref) => document.querySelector('[' + REF + '="' + Number(ref) + '"]');
  return {
    read(maxText) {
      const sel = 'a[href], button, input, select, textarea, summary, [role=button], [role=link], [role=checkbox], [role=tab], [role=menuitem], [contenteditable=""], [contenteditable=true]';
      let n = 0;
      document.querySelectorAll('[' + REF + ']').forEach((el) => el.removeAttribute(REF));
      const elements = [];
      for (const el of document.querySelectorAll(sel)) {
        if (!visible(el) || el.disabled || (el.tagName === 'INPUT' && el.type === 'hidden')) continue;
        n++;
        el.setAttribute(REF, String(n));
        const e = { ref: n, tag: el.tagName.toLowerCase(), name: label(el) };
        const role = el.getAttribute('role');
        if (role) e.role = role;
        if (el.tagName === 'INPUT') e.type = (el.getAttribute('type') || 'text').toLowerCase();
        if (secret(el)) e.secret = true;
        if (el.type === 'checkbox' || el.type === 'radio') e.checked = el.checked;
        if (el.tagName === 'A') e.href = el.href.slice(0, 200);
        elements.push(e);
        if (n >= 300) break;
      }
      const text = (document.body ? document.body.innerText : '').replace(/\n{3,}/g, '\n\n');
      return JSON.stringify({ url: location.href, title: document.title, text: text.slice(0, maxText), cut: text.length > maxText, elements });
    },
    // where returns the centre of an element, scrolled into view, and
    // whether it's a field the agent may not type into.
    where(ref) {
      const el = byRef(ref);
      if (!el) return JSON.stringify({ missing: true });
      el.scrollIntoView({ block: 'center', inline: 'center' });
      const r = el.getBoundingClientRect();
      const editable = /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable;
      return JSON.stringify({ x: r.left + r.width / 2, y: r.top + r.height / 2, secret: secret(el), editable });
    },
    // focused says whether the focused field is one the agent may not type into.
    focused() {
      return JSON.stringify({ secret: secret(focusedEl()) });
    },
    mask(on) {
      const id = 'sky-mask';
      const old = document.getElementById(id);
      if (old) old.remove();
      if (!on) return '{}';
      const s = document.createElement('style');
      s.id = id;
      // Every field and frame: a password can be shown as text, and a code
      // field needn't say what it is.
      s.textContent = 'input, textarea, select, [contenteditable], iframe, frame { filter: blur(10px) !important; }';
      document.documentElement.appendChild(s);
      return '{}';
    },
  };
})()
