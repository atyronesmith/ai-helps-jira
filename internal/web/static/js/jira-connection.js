(() => {
    'use strict';

    const button = document.getElementById('jira-test-connection');
    const result = document.getElementById('jira-connection-result');
    if (!button || !result) return;

    const TIMEOUT_MS = 12000;
    let running = false;

    function render(payload, success) {
        const message = payload.message;
        const checkedAt = new Date(payload.checked_at).toLocaleString();
        const latency = `${payload.latency_ms} ms`;
        result.textContent = `${success ? 'Jira connection succeeded' : 'Jira connection failed'}: ${message} · checked ${checkedAt} · ${latency}`;
        result.setAttribute('role', success ? 'status' : 'alert');
    }

    async function checkConnection() {
        if (running) return;
        running = true;
        button.disabled = true;
        button.setAttribute('aria-busy', 'true');
        result.textContent = 'Testing Jira connection…';
        result.setAttribute('role', 'status');

        const controller = new AbortController();
        let timedOut = false;
        const timeout = setTimeout(() => {
            timedOut = true;
            controller.abort();
        }, TIMEOUT_MS);

        try {
            const response = await fetch('/api/jira/test-connection', {
                method: 'POST',
                headers: { 'X-Jira-Connection-Test': '1' },
                cache: 'no-store',
                signal: controller.signal,
            });
            const payload = await response.json();
            if (!payload || typeof payload.ok !== 'boolean' || typeof payload.state !== 'string' ||
                typeof payload.reachable !== 'boolean' || typeof payload.authenticated !== 'boolean' ||
                typeof payload.message !== 'string' || typeof payload.checked_at !== 'string' ||
                !Number.isFinite(Date.parse(payload.checked_at)) ||
                !Number.isFinite(payload.latency_ms) || payload.latency_ms < 0) {
                throw new Error('invalid response');
            }

            const success = response.status === 200 && payload.ok && payload.reachable && payload.authenticated;
            render(payload, success);
            window.dispatchEvent(new CustomEvent('jira-connection-tested'));
        } catch (_) {
            const message = timedOut ? 'The Jira connection check timed out.' : 'Could not complete the Jira connection check.';
            result.textContent = message;
            result.setAttribute('role', 'alert');
        } finally {
            clearTimeout(timeout);
            running = false;
            button.disabled = false;
            button.removeAttribute('aria-busy');
        }
    }

    button.addEventListener('click', checkConnection);
})();
