const app = document.getElementById('app');
const tg = window.Telegram?.WebApp;
let cfg = {};

// ---------- plumbing ----------

const esc = (s) =>
  String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const el = (html) => {
  const t = document.createElement('template');
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
};

const bytes = (n) => {
  if (n < 1024) return `${n} B`;
  if (n < 1048576) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1048576).toFixed(1)} MB`;
};

async function api(method, path, body) {
  const headers = {};
  // initData is the whole auth story. Sent on every call so the server can
  // check it per-request rather than minting a session of its own.
  if (tg?.initData) headers['X-Telegram-Init-Data'] = tg.initData;
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

const show = (node) => {
  app.replaceChildren(node);
};

const fatal = (msg) => show(el(`<div class="center"><p class="error">${esc(msg)}</p></div>`));

// ---------- home ----------

function home() {
  const view = el(`
    <div>
      <h1>charon</h1>
      <p class="sub">One-way delivery. Nothing is written to disk, nothing survives a restart.</p>
      <div class="tabs" role="tablist">
        <button role="tab" data-mode="send" aria-selected="true">Send a secret</button>
        <button role="tab" data-mode="request" aria-selected="false">Request one</button>
      </div>
      <form id="builder"></form>
    </div>`);

  const tabs = view.querySelectorAll('[role=tab]');
  let mode = 'send';
  const render = () => {
    tabs.forEach((t) => t.setAttribute('aria-selected', String(t.dataset.mode === mode)));
    builder(view.querySelector('#builder'), mode);
  };
  tabs.forEach((t) =>
    t.addEventListener('click', () => {
      mode = t.dataset.mode;
      render();
    })
  );
  render();
  show(view);
}

// builder draws the create form for whichever mode is active. Send mode
// collects values inline; request mode collects descriptions of what it wants.
function builder(form, mode) {
  const sending = mode === 'send';
  form.replaceChildren(
    el(`
    <div>
      <div class="field">
        <label for="title">Title</label>
        <input type="text" id="title" required placeholder="${sending ? 'Staging database password' : 'Credentials for the deploy'}">
      </div>
      <div class="field">
        <label for="desc">Description <span class="saving">markdown, optional</span></label>
        <textarea id="desc" placeholder="${sending ? 'Anything the recipient should know.' : 'Why you need these, and where they come from.'}"></textarea>
      </div>
      <div id="items"></div>
      <button type="button" class="ghost" id="add">+ Add ${sending ? 'secret' : 'requested item'}</button>
      <div class="field" style="margin-top:1.2rem">
        <label for="ttl">Expires after</label>
        <select id="ttl">${(cfg.ttl_options || ['1h']).map((o) => `<option${o === '1h' ? ' selected' : ''}>${o}</option>`).join('')}</select>
      </div>
      <button type="submit" class="primary">${sending ? 'Create link' : 'Create request'}</button>
      <p class="note" id="err"></p>
    </div>`)
  );

  const items = form.querySelector('#items');
  const addRow = () => {
    const i = items.children.length;
    const row = el(
      sending
        ? `<div class="item">
             <div class="field">
               <label>Name <button type="button" class="ghost" data-rm style="float:right;font-weight:400">remove</button></label>
               <input type="text" data-name placeholder="DB_PASSWORD" required>
             </div>
             <div class="field">
               <label>Value</label>
               <textarea data-value placeholder="paste the secret"></textarea>
             </div>
             <div class="field">
               <label>Files <span class="saving">up to ${bytes(cfg.max_file_bytes || 0)} each</span></label>
               <input type="file" data-files multiple>
             </div>
           </div>`
        : `<div class="item">
             <div class="field">
               <label>Name <button type="button" class="ghost" data-rm style="float:right;font-weight:400">remove</button></label>
               <input type="text" data-name placeholder="DO_API_TOKEN" required>
             </div>
             <div class="field">
               <label>What you need <span class="saving">markdown, optional</span></label>
               <textarea data-desc placeholder="read+write scope, no expiry"></textarea>
             </div>
             <div class="field">
               <label>Kind</label>
               <select data-type><option value="text">Text</option><option value="file">File</option></select>
             </div>
           </div>`
    );
    row.querySelector('[data-rm]').addEventListener('click', () => {
      if (items.children.length > 1) row.remove();
    });
    items.append(row);
    void i;
  };
  addRow();
  form.querySelector('#add').addEventListener('click', addRow);

  form.onsubmit = async (ev) => {
    ev.preventDefault();
    const btn = form.querySelector('button[type=submit]');
    const err = form.querySelector('#err');
    btn.disabled = true;
    err.textContent = '';
    try {
      const rows = [...items.children];
      const spec = {
        title: form.querySelector('#title').value,
        description: form.querySelector('#desc').value,
        ttl: form.querySelector('#ttl').value,
        items: rows.map((r) => ({
          name: r.querySelector('[data-name]').value,
          description: r.querySelector('[data-desc]')?.value || '',
          type: r.querySelector('[data-type]')?.value || (r.querySelector('[data-files]')?.files.length ? 'file' : 'text'),
        })),
      };
      const created = await api('POST', sending ? '/api/secrets' : '/api/requests', spec);

      if (!sending) return created_links(created, 'request');

      // Send mode fills the draft it just created, then submits it, so both
      // modes share one server-side path.
      const id = created.submit_url.split('/').pop();
      for (const [i, r] of rows.entries()) {
        const text = r.querySelector('[data-value]').value;
        if (text) await api('PUT', `/api/e/${id}/text/${i}`, { text });
        for (const f of r.querySelector('[data-files]').files) {
          const fd = new FormData();
          fd.append('file', f, f.name);
          await api('POST', `/api/e/${id}/files/${i}`, fd);
        }
      }
      await api('POST', `/api/e/${id}/submit`);
      created_links(created, 'send');
    } catch (e) {
      err.className = 'error';
      err.textContent = e.message;
      btn.disabled = false;
    }
  };
}

function created_links(created, mode) {
  const share = mode === 'send' ? created.retrieve_url : created.submit_url;
  const view = el(`
    <div>
      <h1>Ready</h1>
      <p class="sub">Expires ${new Date(created.expires_at).toLocaleString()}.</p>
      <div class="card">
        <label style="font-size:.8rem;font-weight:560">${mode === 'send' ? 'Send this link to the recipient' : 'Send this link to whoever has the secrets'}</label>
        <div class="linkbox">
          <input type="text" readonly value="${esc(share)}">
          <button class="ghost" data-copy="${esc(share)}">Copy</button>
        </div>
        ${
          created.telegram_url && mode === 'request'
            ? `<p class="note">Telegram: <a href="${esc(created.telegram_url)}">${esc(created.telegram_url)}</a></p>`
            : ''
        }
      </div>
      ${
        mode === 'request'
          ? `<div class="card">
               <label style="font-size:.8rem;font-weight:560">Keep this one — it is how you read the answer</label>
               <div class="linkbox">
                 <input type="text" readonly value="${esc(created.retrieve_url)}">
                 <button class="ghost" data-copy="${esc(created.retrieve_url)}">Copy</button>
               </div>
             </div>`
          : ''
      }
      <p class="note">The secret self-destructs ${cfg.linger || '60s'} after it is first read.</p>
      <p class="note"><a href="/">Start another</a></p>
    </div>`);

  view.querySelectorAll('[data-copy]').forEach((b) =>
    b.addEventListener('click', async () => {
      await navigator.clipboard.writeText(b.dataset.copy);
      b.textContent = 'Copied';
      setTimeout(() => (b.textContent = 'Copy'), 1200);
    })
  );
  show(view);
}

// ---------- entry: submit side ----------

function submitForm(id, e) {
  const view = el(`
    <div>
      <h1>${esc(e.title)}</h1>
      ${e.description_html ? `<div class="md" style="margin-bottom:1.3rem">${e.description_html}</div>` : '<p class="sub">Fill these in and submit.</p>'}
      <form id="f"></form>
    </div>`);
  const form = view.querySelector('#f');

  e.items.forEach((it, i) => {
    const row = el(`
      <div class="item">
        <div class="field">
          <label>${esc(it.name)} <span class="saving" data-status></span></label>
          ${it.description_html ? `<div class="md">${it.description_html}</div>` : ''}
          ${it.type === 'file' ? '' : '<textarea data-text></textarea>'}
        </div>
        <div class="field">
          <label style="font-weight:400;color:var(--muted)">Attach files</label>
          <input type="file" data-file multiple>
          <ul class="files" data-list></ul>
        </div>
      </div>`);

    const status = row.querySelector('[data-status]');
    const list = row.querySelector('[data-list]');

    const drawFiles = (names) => {
      list.replaceChildren(
        ...names.map((n, j) => {
          const li = el(`<li><span class="name">${esc(n)}</span><button type="button" class="ghost" data-rm>remove</button></li>`);
          li.querySelector('[data-rm]').addEventListener('click', async () => {
            await api('DELETE', `/api/e/${id}/files/${i}/${j}`);
            const fresh = await api('GET', `/api/e/${id}`);
            drawFiles(fresh.items[i].files || []);
          });
          return li;
        })
      );
    };
    drawFiles(it.files || []);

    // Autosave. The point of the draft is that Telegram can suspend this
    // webview mid-form and nothing is lost, so every keystroke pause commits.
    const ta = row.querySelector('[data-text]');
    if (ta) {
      ta.value = it.text || '';
      let timer;
      ta.addEventListener('input', () => {
        status.textContent = 'saving…';
        clearTimeout(timer);
        timer = setTimeout(async () => {
          try {
            await api('PUT', `/api/e/${id}/text/${i}`, { text: ta.value });
            status.textContent = 'saved';
          } catch (err) {
            status.textContent = err.message;
          }
        }, 400);
      });
    }

    row.querySelector('[data-file]').addEventListener('change', async (ev) => {
      const input = ev.target;
      for (const f of input.files) {
        status.textContent = `uploading ${f.name}…`;
        const fd = new FormData();
        fd.append('file', f, f.name);
        try {
          await api('POST', `/api/e/${id}/files/${i}`, fd);
        } catch (err) {
          status.textContent = err.message;
          return;
        }
      }
      input.value = '';
      status.textContent = 'saved';
      const fresh = await api('GET', `/api/e/${id}`);
      drawFiles(fresh.items[i].files || []);
    });

    form.append(row);
  });

  const send = el('<button type="submit" class="primary" style="margin-top:1.2rem">Submit</button>');
  const err = el('<p class="note"></p>');
  form.append(send, err);

  form.onsubmit = async (ev) => {
    ev.preventDefault();
    send.disabled = true;
    try {
      await api('POST', `/api/e/${id}/submit`);
      show(
        el(`<div class="center"><h1>Submitted</h1><p class="sub">The requester can read it once. You can close this.</p></div>`)
      );
      tg?.close?.();
    } catch (e2) {
      err.className = 'error';
      err.textContent = e2.message;
      send.disabled = false;
    }
  };
  show(view);
}

// ---------- entry: retrieve side ----------

function retrieveView(id, e) {
  if (!e.fulfilled) {
    const view = el(`
      <div class="center">
        <h1>${esc(e.title)}</h1>
        <p class="sub">Waiting for the other side to submit…</p>
      </div>`);
    show(view);
    // Poll rather than long-poll: one endpoint, no connection held open, and
    // the cost of a request per second on a LAN service is nil.
    setTimeout(async () => {
      try {
        const fresh = await api('GET', `/api/e/${id}`);
        if (fresh.fulfilled) retrieveView(id, fresh);
        else retrieveView(id, fresh);
      } catch {
        fatal('This link has expired.');
      }
    }, 2000);
    return;
  }

  const view = el(`
    <div>
      <h1>${esc(e.title)}</h1>
      ${e.description_html ? `<div class="md" style="margin-bottom:1.3rem">${e.description_html}</div>` : ''}
      <p class="sub">Reading this destroys it shortly afterwards.</p>
      <button class="primary" id="reveal">Reveal</button>
    </div>`);
  view.querySelector('#reveal').addEventListener('click', async () => {
    try {
      const got = await api('POST', `/api/e/${id}/retrieve`);
      revealed(got);
    } catch (err) {
      fatal(err.message);
    }
  });
  show(view);
}

function revealed(got) {
  const view = el(`<div><h1>${esc(got.title)}</h1><p class="sub">Destroyed ${new Date(got.destructs_at).toLocaleTimeString()}.</p></div>`);
  for (const it of got.items) {
    const card = el(`<div class="card"><label style="font-size:.8rem;font-weight:560">${esc(it.name)}</label></div>`);
    if (it.text) {
      const pre = el(`<pre class="value"></pre>`);
      pre.textContent = it.text;
      const copy = el('<button class="ghost" style="margin-top:.5rem">Copy</button>');
      copy.addEventListener('click', async () => {
        await navigator.clipboard.writeText(it.text);
        copy.textContent = 'Copied';
      });
      card.append(pre, copy);
    }
    for (const f of it.files || []) {
      card.append(
        el(`<ul class="files"><li><span class="name"><a href="${esc(f.url)}">${esc(f.filename)}</a></span><span class="size">${bytes(f.size)}</span></li></ul>`)
      );
    }
    view.append(card);
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
  const startParam = tg?.initDataUnsafe?.start_param;
  const match = location.pathname.match(/^\/e\/([a-z2-7]+)$/);
  const id = match?.[1] || startParam;

  if (!id) return home();

  try {
    const e = await api('GET', `/api/e/${id}`);
    if (e.role === 'submit') {
      if (e.fulfilled) return fatal('This has already been submitted.');
      return submitForm(id, e);
    }
    retrieveView(id, e);
  } catch (err) {
    fatal(err.status === 404 ? 'This link has expired or was already used.' : err.message);
  }
}

boot();
