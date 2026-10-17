const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const script = fs.readFileSync('internal/web/static/js/dashboard.js', 'utf8');

class Element {
  constructor(id = '', classes = []) {
    this.id = id;
    this.classes = new Set(classes);
    this.dataset = {};
    this.checked = false;
    this.disabled = false;
    this.style = {};
    this.attributes = {};
    this.listeners = {};
    this.childNodes = [];
    this.textContent = '';
    this.parent = null;
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(key, value) { this.attributes[key] = value; }
  matches(selector) {
    if (selector.startsWith('.')) return this.classes.has(selector.slice(1));
    return this.id === selector.slice(1);
  }
  remove() { if (this.parent) this.parent.childNodes = this.parent.childNodes.filter((node) => node !== this); }
  replaceChildren(...children) {
    this.childNodes = children;
    children.forEach((child) => { child.parent = this; });
  }
}

function makeRow(id, selected = false) {
  const row = new Element(`row-${id}`);
  const checkbox = new Element('', ['row-check']);
  checkbox.dataset.id = id;
  checkbox.checked = selected;
  checkbox.parent = row;
  row.childNodes = [checkbox];
  row.checkbox = checkbox;
  return row;
}

class Harness {
  constructor(storage = {}) {
    this.elements = new Map();
    this.listeners = {};
    this.intervals = [];
    this.requests = [];
    this.storage = storage;
    this.hidden = false;
    for (const [id, classes] of [
      ['refresh-now', []], ['auto-refresh', []], ['bulk-delete-btn', []],
      ['dashboard-update', []], ['dashboard-results', []], ['select-all', []],
    ]) this.elements.set(id, new Element(id, classes));
    this.document = {
      hidden: false,
      getElementById: (id) => this.elements.get(id) || this.findRow(id),
      querySelectorAll: (selector) => {
        if (selector === '.row-check') return this.rows().map((row) => row.checkbox);
        if (selector === '.row-check:checked') return this.rows().map((row) => row.checkbox).filter((c) => c.checked);
        return [];
      },
      addEventListener: (type, fn) => { (this.listeners[type] ||= []).push(fn); },
      importNode: (node) => node.clone(),
    };
    this.results = this.elements.get('dashboard-results');
    this.window = {};
    const harness = this;
    this.context = {
      document: this.document,
      window: this.window,
      DOMParser: class {
        parseFromString(html) {
          const match = html.match(/<div id="dashboard-results">([\s\S]*?)<\/div>/);
          const result = new Element('dashboard-results');
          result.childNodes = [...(match ? match[1].matchAll(/<tr id="row-([^" ]+)"[\s\S]*?<input type="checkbox" class="row-check" data-id="([^" ]+)"[^>]*>/g) : [])]
            .map((m) => makeRow(m[2]));
          result.isEmpty = !match || /No results yet/.test(match[1]);
          if (result.isEmpty) result.childNodes = [new Element('empty-state')];
          result.clone = () => {
            const copy = new Element('dashboard-results');
            copy.childNodes = result.childNodes.map((row) => row.id === 'empty-state' ? new Element('empty-state') : makeRow(row.checkbox.dataset.id));
            return copy;
          };
          return { getElementById: (id) => id === 'dashboard-results' && match ? result : null };
        }
      },
      AbortController,
      fetch: (...args) => new Promise((resolve, reject) => harness.requests.push({ args, resolve, reject })),
      confirm: () => true,
      setInterval: (fn, ms) => { harness.intervals.push({ fn, ms }); },
      localStorage: {
        getItem(key) { if (harness.storage.throw) throw new Error('blocked'); return harness.storage[key] ?? null; },
        setItem(key, value) { if (harness.storage.throw) throw new Error('blocked'); harness.storage[key] = value; },
      },
      console,
      Set,
      Error,
    };
    vm.runInNewContext(script, this.context, { filename: 'dashboard.js' });
  }
  rows() { return this.results.childNodes.filter((node) => node.id.startsWith('row-')); }
  findRow(id) { return this.rows().find((row) => row.id === id) || null; }
  event(target) { for (const fn of this.listeners.change || []) fn({ target }); }
  trigger(target, type) { return (target.listeners[type] || []).map((fn) => fn({ target })); }
  respond(index, html, status = 200) {
    this.requests[index].resolve({ ok: status >= 200 && status < 300, status, text: async () => html });
  }
  reject(index, error) { this.requests[index].reject(error); }
  async flush() { await new Promise((resolve) => setImmediate(resolve)); }
  startManual() { return this.trigger(this.elements.get('refresh-now'), 'click')[0]; }
  html(rows, empty = false) {
    const body = empty ? '<p>No results yet. Use Claude Code to call a jira-cli tool and results will appear here.</p>' : rows.map((id) => `<tr id="row-${id}"><input type="checkbox" class="row-check" data-id="${id}"></tr>`).join('');
    return `<div id="dashboard-results">${body}</div>`;
  }
}

async function main() {
  {
    const h = new Harness();
    h.results.replaceChildren(makeRow('old', true));
    h.startManual();
    assert.equal(h.requests[0].args[0], '/');
    assert.equal(h.requests[0].args[1].cache, 'no-store');
    h.rows()[0].checkbox.checked = false;
    h.event(h.rows()[0].checkbox);
    h.respond(0, h.html(['old', 'new']));
    await h.flush();
    assert.deepEqual(h.rows().map((row) => [row.checkbox.dataset.id, row.checkbox.checked]), [['old', false], ['new', false]]);
    assert.equal(h.elements.get('dashboard-update').textContent, 'Results refreshed.');
  }
  {
    const h = new Harness();
    h.results.replaceChildren(new Element('empty-state'));
    h.startManual();
    h.respond(0, h.html(['fresh']));
    await h.flush();
    assert.deepEqual(h.rows().map((row) => row.checkbox.dataset.id), ['fresh']);
    h.startManual();
    h.respond(1, h.html([], true));
    await h.flush();
    assert.equal(h.rows().length, 0);
    assert.equal(h.results.childNodes[0].id, 'empty-state');
  }
  {
    const h = new Harness();
    assert.equal(h.elements.get('auto-refresh').checked, false);
    assert.equal(h.intervals[0].ms, 15000);
    h.elements.get('auto-refresh').checked = true;
    h.trigger(h.elements.get('auto-refresh'), 'change');
    assert.equal(h.storage['jira-cli.dashboard.auto-refresh'], 'true');
    h.elements.get('auto-refresh').checked = false;
    h.trigger(h.elements.get('auto-refresh'), 'change');
    assert.equal(h.storage['jira-cli.dashboard.auto-refresh'], 'false');
    const saved = new Harness({ 'jira-cli.dashboard.auto-refresh': 'true' });
    assert.equal(saved.elements.get('auto-refresh').checked, true);
    const unavailable = new Harness({ throw: true });
    assert.equal(unavailable.elements.get('auto-refresh').checked, false);
  }
  {
    const h = new Harness({ 'jira-cli.dashboard.auto-refresh': 'true' });
    h.document.hidden = true;
    h.intervals[0].fn();
    assert.equal(h.requests.length, 0);
    h.document.hidden = false;
    h.intervals[0].fn();
    h.intervals[0].fn();
    assert.equal(h.requests.length, 1, 'poll skips hidden tabs and overlapping requests');
    h.elements.get('auto-refresh').checked = false;
    h.trigger(h.elements.get('auto-refresh'), 'change');
    h.respond(0, h.html(['ignored']));
    await h.flush();
    assert.equal(h.rows().length, 0, 'disabling auto refresh discards pending automatic response');
    h.intervals[0].fn();
    assert.equal(h.requests.length, 1, 'disabled auto refresh does not start later polls');
  }
  {
    const h = new Harness();
    h.results.replaceChildren(makeRow('kept'));
    h.startManual();
    h.reject(0, new Error('network unavailable'));
    await h.flush();
    assert.deepEqual(h.rows().map((row) => row.checkbox.dataset.id), ['kept']);
    assert.equal(h.elements.get('dashboard-update').attributes.role, 'alert');
    assert.match(h.elements.get('dashboard-update').textContent, /network unavailable/);
    h.startManual();
    h.respond(1, '<div>invalid</div>');
    await h.flush();
    assert.deepEqual(h.rows().map((row) => row.checkbox.dataset.id), ['kept']);
    h.startManual();
    assert.equal(h.requests.length, 3, 'manual refresh remains available after errors');
    h.respond(2, h.html(['recovered']));
    await h.flush();
    assert.deepEqual(h.rows().map((row) => row.checkbox.dataset.id), ['recovered']);
  }
  {
    const h = new Harness();
    h.startManual();
    h.elements.get('auto-refresh').checked = true;
    h.trigger(h.elements.get('auto-refresh'), 'change');
    h.elements.get('auto-refresh').checked = false;
    h.trigger(h.elements.get('auto-refresh'), 'change');
    h.respond(0, h.html(['manual-result']));
    await h.flush();
    assert.deepEqual(h.rows().map((row) => row.checkbox.dataset.id), ['manual-result'], 'auto preference changes do not cancel a manual refresh');
  }
  {
    const h = new Harness();
    h.results.replaceChildren(makeRow('delete-me', true));
    const pending = h.trigger(h.elements.get('bulk-delete-btn'), 'click')[0];
    assert.equal(h.requests[0].args[0], '/api/result/delete-me');
    h.startManual();
    assert.equal(h.requests.length, 1, 'refresh cannot begin during deletion');
    h.requests[0].resolve({ ok: true, status: 204 });
    await pending;
    assert.equal(h.rows().length, 0);
    h.startManual();
    assert.equal(h.requests.length, 2, 'refresh can run after deletion');
    h.respond(1, h.html(['survivor']));
    await h.flush();
    assert.deepEqual(h.rows().map((row) => row.checkbox.dataset.id), ['survivor']);
    h.elements.get('select-all').checked = true;
    h.event(h.elements.get('select-all'));
    assert.equal(h.rows()[0].checkbox.checked, true, 'delegated select-all handles newly rendered rows');
    h.rows()[0].checkbox.checked = false;
    h.event(h.rows()[0].checkbox);
    assert.equal(h.elements.get('bulk-delete-btn').style.display, 'none', 'delegated row changes update delete control');
    h.rows()[0].checkbox.checked = true;
    h.event(h.rows()[0].checkbox);
    const deleteAfterRefresh = h.trigger(h.elements.get('bulk-delete-btn'), 'click')[0];
    h.requests[2].resolve({ ok: true, status: 204 });
    await deleteAfterRefresh;
    assert.equal(h.rows().length, 0, 'delegated bulk-delete works after fragment replacement');
  }
  {
    const h = new Harness();
    h.results.replaceChildren(makeRow('retry-me', true));
    const deleting = h.trigger(h.elements.get('bulk-delete-btn'), 'click')[0];
    h.requests[0].resolve({ ok: false, status: 503 });
    await deleting;
    assert.equal(h.rows()[0].checkbox.checked, true, 'failed deletion remains selected');
    assert.equal(h.elements.get('dashboard-update').attributes.role, 'alert');
    assert.match(h.elements.get('dashboard-update').textContent, /Failed selections remain selected/);
  }
  console.log('dashboard refresh regressions passed');
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
