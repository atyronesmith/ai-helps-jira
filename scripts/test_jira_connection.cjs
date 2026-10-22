const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const script = fs.readFileSync('internal/web/static/js/jira-connection.js', 'utf8');
const layout = fs.readFileSync('internal/web/templates/layout.html', 'utf8');

class Element {
  constructor(id) {
    this.id = id;
    this.disabled = false;
    this.attributes = {};
    this.listeners = {};
    this.textContent = '';
    this.innerHTML = '';
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  setAttribute(key, value) { this.attributes[key] = value; }
  removeAttribute(key) { delete this.attributes[key]; }
  click() { return (this.listeners.click || []).map((fn) => fn({ target: this })); }
}

class Harness {
  constructor() {
    this.button = new Element('jira-test-connection');
    this.result = new Element('jira-connection-result');
    this.requests = [];
    this.timers = [];
    this.events = [];
    this.document = { getElementById: (id) => ({
      'jira-test-connection': this.button,
      'jira-connection-result': this.result,
    })[id] || null };
    const harness = this;
    this.context = {
      document: this.document,
      window: { dispatchEvent: (event) => harness.events.push(event.type) },
      CustomEvent: class { constructor(type) { this.type = type; } },
      AbortController,
      Date,
      Number,
      fetch: (...args) => new Promise((resolve, reject) => {
        const request = { args, resolve, reject };
        harness.requests.push(request);
        args[1].signal.addEventListener('abort', () => {
          const error = new Error('aborted');
          error.name = 'AbortError';
          reject(error);
        });
      }),
      setTimeout: (fn, ms) => {
        const timer = { fn, ms, cleared: false };
        harness.timers.push(timer);
        return timer;
      },
      clearTimeout: (timer) => { timer.cleared = true; },
    };
    vm.runInNewContext(script, this.context, { filename: 'jira-connection.js' });
  }
  click() { return this.button.click()[0]; }
  respond(index, payload, status = 200, jsonError = null) {
    this.requests[index].resolve({
      ok: status >= 200 && status < 300,
      status,
      json: async () => { if (jsonError) throw jsonError; return payload; },
    });
  }
  async flush() { await new Promise((resolve) => setImmediate(resolve)); }
}

const success = {
  ok: true, state: 'connected', reachable: true, authenticated: true,
  message: 'Jira is reachable and authenticated.',
  checked_at: '2026-10-20T12:30:00Z', latency_ms: 42,
};

async function main() {
  assert.match(layout, /id="jira-test-connection" type="button">Test Jira connection/);
  assert.match(layout, /id="jira-connection-result" role="status" aria-live="polite"/);
  assert.match(layout, /project permissions are not checked/i);
  assert.match(layout, /src="\/static\/js\/jira-connection\.js" defer/);
  assert.match(layout, /window\.addEventListener\('jira-connection-tested', refreshStatus\)/);

  {
    const h = new Harness();
    assert.equal(h.requests.length, 0, 'initialization does not make a request');
    assert.equal(h.timers.length, 0, 'initialization does not schedule a timer');
    const pending = h.click();
    assert.equal(h.button.disabled, true);
    assert.equal(h.button.attributes['aria-busy'], 'true');
    assert.equal(h.timers[0].ms, 12000);
    assert.equal(h.requests.length, 1);
    const [url, options] = h.requests[0].args;
    assert.equal(url, '/api/jira/test-connection');
    assert.equal(options.method, 'POST');
    assert.equal(options.headers['X-Jira-Connection-Test'], '1');
    assert.equal(Object.keys(options.headers).length, 1);
    assert.equal(options.cache, 'no-store');
    assert.ok(options.signal);
    h.click();
    assert.equal(h.requests.length, 1, 'a pending check blocks duplicate clicks');
    h.respond(0, success);
    await pending;
    assert.equal(h.result.attributes.role, 'status');
    assert.match(h.result.textContent, /Jira connection succeeded/);
    assert.match(h.result.textContent, /Jira is reachable and authenticated/);
    assert.ok(h.result.textContent.includes(new Date(success.checked_at).toLocaleString()));
    assert.match(h.result.textContent, /42 ms/);
    assert.deepEqual(h.events, ['jira-connection-tested']);
    assert.equal(h.button.disabled, false);
    assert.equal(h.button.attributes['aria-busy'], undefined);
    assert.equal(h.timers[0].cleared, true);
    const recheck = h.click();
    assert.equal(h.requests.length, 2, 'button works again after completion');
    h.respond(1, success);
    await recheck;
  }
  {
    const h = new Harness();
    const pending = h.click();
    h.respond(0, { ...success, ok: false, authenticated: false, message: 'Authentication failed.' }, 403);
    await pending;
    assert.equal(h.result.attributes.role, 'alert');
    assert.match(h.result.textContent, /Jira connection failed/);
    assert.match(h.result.textContent, /Authentication failed/);
    assert.deepEqual(h.events, ['jira-connection-tested']);
    assert.equal(h.button.disabled, false);
  }
  {
    const h = new Harness();
    const pending = h.click();
    h.respond(0, { ...success, reachable: false, authenticated: false }, 502);
    await pending;
    assert.equal(h.result.attributes.role, 'alert', 'non-2xx JSON stays a failed check');
  }
  {
    const h = new Harness();
    const pending = h.click();
    h.respond(0, success, 201);
    await pending;
    assert.equal(h.result.attributes.role, 'alert', 'only HTTP 200 can report success');
  }
  for (const kind of ['malformed', 'network']) {
    const h = new Harness();
    const pending = h.click();
    if (kind === 'malformed') h.respond(0, null, 200, new Error('private response detail'));
    else h.requests[0].reject(new Error('private network detail'));
    await pending;
    assert.equal(h.result.attributes.role, 'alert');
    assert.match(h.result.textContent, /Could not complete/);
    assert.doesNotMatch(h.result.textContent, /private/);
    assert.equal(h.button.disabled, false);
    assert.equal(h.button.attributes['aria-busy'], undefined);
    assert.deepEqual(h.events, []);
  }
  {
    const h = new Harness();
    const pending = h.click();
    h.timers[0].fn();
    await pending;
    assert.match(h.result.textContent, /timed out/i);
    assert.equal(h.result.attributes.role, 'alert');
    assert.equal(h.button.disabled, false);
    assert.equal(h.button.attributes['aria-busy'], undefined);
    assert.deepEqual(h.events, []);
  }
  {
    const h = new Harness();
    const pending = h.click();
    h.respond(0, { ...success, message: '<img src=x onerror=alert(1)>' });
    await pending;
    assert.match(h.result.textContent, /<img src=x onerror=alert\(1\)>/);
    assert.equal(h.result.innerHTML, '', 'untrusted message is only assigned as text');
  }

  console.log('jira connection regressions passed');
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
