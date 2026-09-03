const app = document.getElementById('app');
const tg = window.Telegram?.WebApp;
let cfg = {};

// ---------- plumbing ----------

const esc = (s) =>
  String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

// Returns the single root element, or a fragment when the template has
// several. Returning only firstElementChild silently dropped the rest, which
// is how a <ul> disappeared and left a null querySelector behind.
const el = (html) => {
  const t = document.createElement('template');
  t.innerHTML = html.trim();
  return t.content.childElementCount === 1 ? t.content.firstElementChild : t.content;
};

const bytes = (n) => {
  if (!n) return '0 B';
  if (n < 1024) return `${n} B`;
  if (n < 1048576) return `${Math.round(n / 1024)} KB`;
  return `${(n / 1048576).toFixed(1)} MB`;
};

// Go renders 60s as "1m0s"; nobody writes that. Build the phrase here instead
// of shipping a duration string to the page.
const lingerText = () => {
  const s = cfg.linger_seconds ?? 60;
  if (s < 60) return `${s} seconds`;
  const m = Math.round(s / 60);
  return m === 1 ? 'a minute' : `${m} minutes`;
};

const ttlLabel = (o) =>
  ({ '15m': '15 min', '1h': '1 hour', '6h': '6 hours', '1d': '1 day', '3d': '3 days', '1w': '1 week' }[o] || o);

async function api(method, path, body) {
  const headers = {};
  if (body && !(body instanceof FormData)) headers['Content-Type'] = 'application/json';

  const res = await fetch(path, {
    method,
    headers,
    body: body instanceof FormData ? body : body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { status: res.status });
  return data;
}

const show = (node) => app.replaceChildren(node);
const fatal = (msg) => show(el(`<div class="center"><h1>Nothing here</h1><p class="lede">${esc(msg)}</p></div>`));

// A field that starts hidden behind a text button. Descriptions are optional
// and usually unused, so they should not take up room until asked for.
function collapsible(buttonText, labelText, placeholder) {
  const wrap = el(`<div>
      <button type="button" class="btn" data-variant="link" data-size="sm">+ ${esc(buttonText)}</button>
      <div role="group" class="field" hidden>
        <label>${esc(labelText)} <span class="hint">markdown</span></label>
        <textarea placeholder="${esc(placeholder)}"></textarea>
      </div>
    </div>`);
  const [btn, body] = [wrap.querySelector('button'), wrap.querySelector('.field')];
  btn.addEventListener('click', () => {
    body.hidden = false;
    btn.hidden = true;
    body.querySelector('textarea').focus();
  });
  wrap.value = () => (body.hidden ? '' : body.querySelector('textarea').value);
  return wrap;
}

// ---------- home ----------

// The landing page is only the choice. Nothing else is on screen until you
// have made it, so the first thing you see is one decision rather than a form.
function home() {
  const view = el(`
    <div>
      <h1>charon</h1>
      <p class="lede">Ephemeral secret sharing.</p>
      <div class="nav">
        <button type="button" class="card" data-mode="request">
          <span class="t">Request</span><span class="d">Ask someone for secrets</span>
        </button>
        <button type="button" class="card" data-mode="send">
          <span class="t">Send</span><span class="d">Hand over your own</span>
        </button>
      </div>
    </div>`);
  view.querySelectorAll('[data-mode]').forEach((b) =>
    b.addEventListener('click', () => compose(b.dataset.mode))
  );
  show(view);
}

// The chooser is replaced by the form for the mode you picked.
function compose(mode) {
  const requesting = mode === 'request';
  const view = el(`
    <div>
      <button type="button" class="btn back" data-variant="link" data-size="sm">&larr; Back</button>
      <h1>${requesting ? 'Request secrets' : 'Send a secret'}</h1>
      <p class="lede">${requesting ? 'Ask someone to fill these in.' : 'Hand these over on a one-time link.'}</p>
      <form id="builder"></form>
    </div>`);
  view.querySelector('.back').addEventListener('click', home);
  builder(view.querySelector('#builder'), mode);
  show(view);
}

function builder(form, mode) {
  const requesting = mode === 'request';

  const view = el(`
    <div>
      <section>
        <div class="stack">
          <div role="group" class="field">
            <label for="title">${requesting ? 'Request name' : 'Secret name'} <span class="hint">optional</span></label>
            <input type="text" id="title" placeholder="a name is generated if you skip this"
                   autocomplete="off" autocapitalize="sentences">
          </div>
          <div id="desc-slot"></div>
        </div>
      </section>

      <section>
        <p class="rubric">Secrets</p>
        <div class="adders">
          <span class="lbl">Add:</span>
          <button type="button" class="btn" data-variant="outline" data-size="sm" data-add="text">Text</button>
          <button type="button" class="btn" data-variant="outline" data-size="sm" data-add="file">File</button>
        </div>
        <div id="rows"></div>
      </section>

      <section>
        <p class="rubric">Expires after</p>
        <div class="segmented" id="ttl" role="radiogroup" aria-label="Expires after"></div>
      </section>

      <section>
        <button type="submit" class="btn btn-block">${requesting ? 'Create request link' : 'Create secret link'}</button>
        <div class="alert" data-variant="destructive" id="err" hidden><p></p></div>
      </section>
    </div>`);
  form.replaceChildren(view);

  const desc = collapsible(
    'add description',
    'Description',
    requesting ? 'Why you need these, and where to find them.' : 'Anything the recipient should know.'
  );
  view.querySelector('#desc-slot').append(desc);

  // Expiry as a radio group of buttons.
  let ttl = cfg.default_ttl || (cfg.ttl_options || ['1d'])[0];
  const ttlBox = view.querySelector('#ttl');
  const drawTTL = () =>
    ttlBox.replaceChildren(
      ...(cfg.ttl_options || ['1d']).map((o) => {
        const b = el(`<button type="button" class="btn" data-size="sm" role="radio"
            data-variant="${o === ttl ? 'secondary' : 'outline'}" aria-checked="${o === ttl}">${esc(ttlLabel(o))}</button>`);
        b.addEventListener('click', () => {
          ttl = o;
          drawTTL();
        });
        return b;
      })
    );
  drawTTL();

  // Secrets start empty: the two Add buttons are the instruction, so there is
  // no half-filled row to explain or delete.
  const rows = view.querySelector('#rows');
  const empty = el(`<section class="empty">
      <header>
        <h3>No secrets yet</h3>
        <p>Add a text value or a file using the buttons above.</p>
      </header>
    </section>`);
  rows.append(empty);

  const renumber = () =>
    [...rows.querySelectorAll('.row')].forEach((r, i) => {
      r.querySelector('[data-name]').placeholder = `secret-${i + 1}`;
    });

  const addRow = (type) => {
    empty.remove();
    const row = el(`
      <div class="card row" data-type="${type}">
        <div class="row-head">
          <input type="text" class="input" data-name placeholder="secret-1" aria-label="Secret name"
                 autocomplete="off" autocapitalize="off" spellcheck="false">
          <span class="badge" data-variant="secondary">${type}</span>
          <button type="button" class="btn" data-variant="ghost" data-size="icon-sm" title="Remove">&times;</button>
        </div>
        <div data-body></div>
      </div>`);

    const body = row.querySelector('[data-body]');
    if (requesting) {
      const d = collapsible('add description', 'What you need', 'read+write scope, no expiry');
      body.append(d);
      row.value = () => ({ description: d.value() });
    } else if (type === 'text') {
      body.append(el(`<textarea class="textarea mono" data-value placeholder="paste the secret"></textarea>`));
      row.value = () => ({ text: body.querySelector('[data-value]').value });
    } else {
      body.append(
        el(`<label class="drop">Choose files<input type="file" data-files multiple></label>
            <div class="item-group" data-list></div>`)
      );
      const input = body.querySelector('[data-files]');
      const list = body.querySelector('[data-list]');
      input.addEventListener('change', () => {
        list.replaceChildren(
          ...[...input.files].map((f) =>
            el(`<article class="item" data-variant="outline" data-size="sm">
                  <section><h3>${esc(f.name)}</h3></section>
                  <aside class="size">${bytes(f.size)}</aside>
                </article>`)
          )
        );
      });
      row.value = () => ({ files: [...input.files] });
    }

    row.querySelector('[title=Remove]').addEventListener('click', () => {
      row.remove();
      if (!rows.querySelector('.row')) rows.append(empty);
      renumber();
    });

    rows.append(row);
    renumber();
    row.querySelector('[data-name]').focus();
  };

  view.querySelectorAll('[data-add]').forEach((b) =>
    b.addEventListener('click', () => addRow(b.dataset.add))
  );

  form.onsubmit = async (ev) => {
    ev.preventDefault();
    const btn = view.querySelector('button[type=submit]');
    const err = view.querySelector('#err');
    const rowEls = [...rows.querySelectorAll('.row')];

    err.hidden = true;
    if (!rowEls.length) {
      err.querySelector('p').textContent = 'Add at least one secret.';
      err.hidden = false;
      return;
    }
    btn.disabled = true;

    try {
      const spec = {
        title: view.querySelector('#title').value,
        description: desc.value(),
        ttl,
        secrets: rowEls.map((r) => ({
          name: r.querySelector('[data-name]').value,
          description: r.value().description || '',
          type: r.dataset.type,
        })),
      };
      const created = await api('POST', requesting ? '/api/requests' : '/api/secrets', spec);
      if (requesting) return location.assign(created.manage_url);

      // Send mode fills the draft it just created and submits it, so both
      // modes travel the same server-side path.
      const id = created.submit_url.split('/').pop();
      for (const [i, r] of rowEls.entries()) {
        const v = r.value();
        if (v.text) await api('PUT', `/api/e/${id}/text/${i}`, { text: v.text });
        for (const f of v.files || []) {
          const fd = new FormData();
          fd.append('file', f, f.name);
          await api('POST', `/api/e/${id}/files/${i}`, fd);
        }
      }
      await api('POST', `/api/e/${id}/submit`);
      location.assign(created.manage_url);
    } catch (e) {
      err.querySelector('p').textContent = e.message;
      err.hidden = false;
      btn.disabled = false;
    }
  };
}

function copyButton(text) {
  const b = el('<button type="button" class="btn" data-variant="ghost" data-size="sm" data-align="end">Copy</button>');
  b.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(text);
      b.textContent = 'Copied';
      setTimeout(() => (b.textContent = 'Copy'), 1400);
    } catch {
      b.textContent = 'Press ⌘C';
    }
  });
  return b;
}

// step numbers the two links a request produces, so it is obvious which one
// goes out and which one you keep.
function linkCard({ step, label, url, keep, note }) {
  const card = el(`<div class="card${keep ? ' keep' : ''}">
      <label class="card-head">${step ? `<span class="step">${step}</span>` : ''}${esc(label)}</label>
      <div class="input-group"><input type="text" readonly value="${esc(url)}"></div>
      ${note ? `<p class="note">${note}</p>` : ''}
    </div>`);
  card.querySelector('.input-group').append(copyButton(url));
  card.querySelector('input').addEventListener('focus', (e) => e.target.select());
  return card;
}

// The manage page. Reached by redirect after creating, and bookmarkable: the
// owner token is in the query string, so a refresh still shows the links.
function manageView(created) {
  const requesting = created.kind !== 'send';
  const view = el(`
    <div>
      <h1>${esc(created.title)}</h1>
      <p class="lede">Expires ${new Date(created.expires_at).toLocaleString()}.</p>
      <section id="links"></section>
      <section>
        <p class="note">Reading the secret destroys it ${lingerText()} later.</p>
        <p class="note"><a href="/">Create another</a></p>
      </section>
    </div>`);
  const links = view.querySelector('#links');

  if (requesting) {
    links.append(
      linkCard({
        step: 1,
        label: 'Send this to whoever has the secrets',
        url: created.submit_url,
      }),
      linkCard({
        step: 2,
        label: 'Keep this — it is how you read the answer',
        url: created.retrieve_url,
        keep: true,
      })
    );
  } else {
    links.append(linkCard({ label: 'Send this to the recipient', url: created.retrieve_url }));
  }
  show(view);
}

// ---------- submit side ----------

function submitForm(id, e) {
  const view = el(`
    <div>
      <h1>${esc(e.title)}</h1>
      ${e.description_html ? `<div class="md lede">${e.description_html}</div>` : '<p class="lede">Fill these in and submit.</p>'}
      <form id="f"><section id="rows"></section></form>
    </div>`);
  const form = view.querySelector('#f');
  const rows = view.querySelector('#rows');

  e.secrets.forEach((sec, i) => {
    const row = el(`
      <div class="card row">
        <div class="row-head">
          <strong style="flex:1;font-size:.9rem">${esc(sec.name)}</strong>
          <span class="status" data-status></span>
        </div>
        ${sec.description_html ? `<div class="md">${sec.description_html}</div>` : ''}
        <div data-body></div>
      </div>`);
    const status = row.querySelector('[data-status]');
    const body = row.querySelector('[data-body]');

    const setStatus = (text, ok) => {
      status.textContent = text;
      status.classList.toggle('ok', !!ok);
    };

    // Render exactly the control the creator asked for. Offering a file box
    // on a text secret (and accepting it) is how a request for a token came
    // back as a token and an unrelated file.
    if (sec.type === 'file') {
      body.append(
        el(`<label class="drop">Attach files<input type="file" data-file multiple></label>
            <div class="item-group" data-list></div>`)
      );
      const list = body.querySelector('[data-list]');

      const drawFiles = (names) =>
        list.replaceChildren(
          ...names.map((n, j) => {
            const li = el(`<article class="item" data-variant="outline" data-size="sm">
                <section><h3>${esc(n)}</h3></section>
                <aside><button type="button" class="btn" data-variant="ghost" data-size="icon-sm" aria-label="Remove">&times;</button></aside>
              </article>`);
            li.querySelector('button').addEventListener('click', async () => {
              await api('DELETE', `/api/e/${id}/files/${i}/${j}`);
              drawFiles((await api('GET', `/api/e/${id}`)).secrets[i].files || []);
            });
            return li;
          })
        );
      drawFiles(sec.files || []);

      body.querySelector('[data-file]').addEventListener('change', async (ev) => {
        const input = ev.target;
        for (const f of input.files) {
          setStatus(`uploading ${f.name}…`);
          const fd = new FormData();
          fd.append('file', f, f.name);
          try {
            await api('POST', `/api/e/${id}/files/${i}`, fd);
          } catch (err) {
            setStatus(err.message);
            return;
          }
        }
        input.value = '';
        setStatus('saved', true);
        drawFiles((await api('GET', `/api/e/${id}`)).secrets[i].files || []);
      });
    } else {
      const ta = el(`<textarea class="textarea mono" placeholder="paste the secret"></textarea>`);
      ta.value = sec.text || '';
      body.append(ta);
      // Autosave: the draft exists so Telegram can suspend this webview
      // mid-form without losing anything, so every typing pause commits.
      let timer;
      ta.addEventListener('input', () => {
        setStatus('saving…');
        clearTimeout(timer);
        timer = setTimeout(async () => {
          try {
            await api('PUT', `/api/e/${id}/text/${i}`, { text: ta.value });
            setStatus('saved', true);
          } catch (err) {
            setStatus(err.message);
          }
        }, 400);
      });
    }

    rows.append(row);
  });

  const send = el('<button type="submit" class="btn btn-block">Submit</button>');
  const err = el('<div class="alert" data-variant="destructive" hidden><p></p></div>');
  const footer = el('<section></section>');
  footer.append(send, err);
  form.append(footer);

  form.onsubmit = async (ev) => {
    ev.preventDefault();
    send.disabled = true;
    err.hidden = true;
    try {
      await api('POST', `/api/e/${id}/submit`);
      show(el(`<div class="center"><h1>Sent</h1><p class="lede">They can read it once. You can close this.</p></div>`));
      setTimeout(() => tg?.close?.(), 1200);
    } catch (e2) {
      err.querySelector('p').textContent = e2.message;
      err.hidden = false;
      send.disabled = false;
    }
  };
  show(view);
}

// ---------- retrieve side ----------

function retrieveView(id, e) {
  if (!e.fulfilled) {
    show(
      el(`<div class="center">
            <h1>${esc(e.title)}</h1>
            <p class="lede">Waiting for the other side to fill this in…</p>
          </div>`)
    );
    // Poll rather than hold a connection open: one endpoint, no long-poll
    // bookkeeping, and a request every couple of seconds costs nothing here.
    setTimeout(async () => {
      try {
        retrieveView(id, await api('GET', `/api/e/${id}`));
      } catch {
        fatal('This link has expired.');
      }
    }, 2000);
    return;
  }

  const view = el(`
    <div>
      <h1>${esc(e.title)}</h1>
      ${e.description_html ? `<div class="md lede">${e.description_html}</div>` : ''}
      <p class="lede">Revealing this destroys it ${lingerText()} later.</p>
      <section><button type="button" class="btn btn-block" id="reveal">Reveal</button></section>
    </div>`);
  view.querySelector('#reveal').addEventListener('click', async (ev) => {
    ev.target.disabled = true;
    try {
      revealed(await api('POST', `/api/e/${id}/retrieve`));
    } catch (err) {
      fatal(err.message);
    }
  });
  show(view);
}

function revealed(got) {
  const view = el(`
    <div>
      <h1>${esc(got.title)}</h1>
      <p class="lede">Gone at ${new Date(got.destructs_at).toLocaleTimeString()}.</p>
      <section id="out"></section>
    </div>`);
  const out = view.querySelector('#out');

  for (const sec of got.secrets) {
    const card = el(`<div class="card"><label class="card-head">${esc(sec.name)}</label></div>`);
    if (sec.text) {
      const pre = el('<pre class="value"></pre>');
      pre.textContent = sec.text;
      const bar = el('<div></div>');
      bar.append(copyButton(sec.text));
      card.append(pre, bar);
    }
    if (!sec.text && !sec.files?.length) {
      card.append(el('<p class="note" style="margin:0">Left empty.</p>'));
    }
    if (sec.files?.length) {
      card.append(
        el(`<div class="item-group">${sec.files
          .map(
            (f) =>
              `<a class="item" data-variant="outline" data-size="sm" href="${esc(f.url)}" download>
                 <section><h3>${esc(f.filename)}</h3></section>
                 <aside class="size">${bytes(f.size)}</aside>
               </a>`
          )
          .join('')}</div>`)
      );
    }
    out.append(card);
  }
  show(view);
}

// ---------- boot ----------

async function boot() {
  tg?.ready?.();
  tg?.expand?.();

  try {
    cfg = await api('GET', '/api/config');
  } catch {
    /* limits are cosmetic in the form; carry on without them */
  }

  // A Mini App opened via t.me/<bot>/<app>?startapp=<id> lands on "/" with the
  // id in start_param, so that is the only way we learn which entry to open.
  const path = location.pathname.match(/^\/e\/([a-z2-7]+)$/);
  const token = new URLSearchParams(location.search).get('token');
  const id = path?.[1] || token || tg?.initDataUnsafe?.start_param;
  if (!id) return home();

  try {
    const e = await api('GET', `/api/e/${id}`);
    switch (e.role) {
      case 'manage':
        return manageView(e);
      case 'submit':
        return e.fulfilled ? fatal('This has already been submitted.') : submitForm(id, e);
      default:
        return retrieveView(id, e);
    }
  } catch (err) {
    fatal(err.status === 404 ? 'This link has expired or was already used.' : err.message);
  }
}

boot();
