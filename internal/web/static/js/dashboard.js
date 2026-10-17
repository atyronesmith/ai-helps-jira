(() => {
    'use strict';

    const AUTO_REFRESH_KEY = 'jira-cli.dashboard.auto-refresh';
    const AUTO_REFRESH_MS = 15000;
    const refreshButton = document.getElementById('refresh-now');
    const autoCheckbox = document.getElementById('auto-refresh');
    const bulkButton = document.getElementById('bulk-delete-btn');
    const updateMessage = document.getElementById('dashboard-update');
    const results = document.getElementById('dashboard-results');
    if (!refreshButton || !autoCheckbox || !bulkButton || !updateMessage || !results) return;

    let refreshing = false;
    let deleting = false;
    let autoGeneration = 0;
    let autoController = null;

    const getRowChecks = (root = document) => [...root.querySelectorAll('.row-check')];
    const selectedIDs = () => getRowChecks().filter((check) => check.checked).map((check) => check.dataset.id);

    function updateBulkButton() {
        const checks = getRowChecks();
        bulkButton.style.display = checks.some((check) => check.checked) ? '' : 'none';
        bulkButton.disabled = refreshing || deleting;
        const selectAll = document.getElementById('select-all');
        if (selectAll) {
            selectAll.checked = checks.length > 0 && checks.every((check) => check.checked);
            selectAll.disabled = deleting;
        }
        checks.forEach((check) => { check.disabled = deleting; });
    }

    function setMessage(message, isError = false) {
        updateMessage.textContent = message;
        updateMessage.setAttribute('role', isError ? 'alert' : 'status');
    }

    function updateControls() {
        refreshButton.disabled = refreshing || deleting;
        autoCheckbox.disabled = deleting;
        updateBulkButton();
    }

    async function refresh(manual) {
        if (refreshing || deleting) return;
        refreshing = true;
        const generation = autoGeneration;
        const controller = new AbortController();
        if (!manual) autoController = controller;
        updateControls();
        setMessage('Refreshing results…');
        try {
            const response = await fetch('/', {
                cache: 'no-store',
                signal: controller.signal,
            });
            if (!response.ok) throw new Error(`Refresh failed (${response.status})`);
            const html = await response.text();
            const parsed = new DOMParser().parseFromString(html, 'text/html');
            const incoming = parsed.getElementById('dashboard-results');
            if (!incoming) throw new Error('Refresh response did not contain results');
            if (!manual && (!autoCheckbox.checked || generation !== autoGeneration)) return;

            // Read selections at apply time so changes made while fetch/parse was pending survive.
            const selected = new Set(selectedIDs());
            const replacement = document.importNode(incoming, true);
            results.replaceChildren(...replacement.childNodes);
            getRowChecks().forEach((check) => { check.checked = selected.has(check.dataset.id); });
            updateBulkButton();
            setMessage('Results refreshed.');
        } catch (error) {
            if (error && error.name === 'AbortError') return;
            if (!manual && (!autoCheckbox.checked || generation !== autoGeneration)) return;
            setMessage(error instanceof Error ? error.message : 'Could not refresh results.', true);
        } finally {
            if (!manual && autoController === controller) autoController = null;
            refreshing = false;
            updateControls();
        }
    }

    async function bulkDelete() {
        if (refreshing || deleting) return;
        const ids = selectedIDs();
        if (ids.length === 0 || !confirm(`Delete ${ids.length} result(s)?`)) return;
        deleting = true;
        updateControls();
        setMessage('Deleting selected results…');
        const failed = [];
        for (const id of ids) {
            try {
                const response = await fetch('/api/result/' + encodeURIComponent(id), { method: 'DELETE' });
                if (!response.ok) throw new Error(`Delete failed (${response.status})`);
                const row = document.getElementById('row-' + id);
                if (row) row.remove();
            } catch (_) {
                failed.push(id);
            }
        }
        deleting = false;
        updateControls();
        if (failed.length) {
            failed.forEach((id) => {
                const checkbox = getRowChecks().find((check) => check.dataset.id === id);
                if (checkbox) checkbox.checked = true;
            });
            updateBulkButton();
            setMessage(`Could not delete ${failed.length} result(s). Failed selections remain selected; retry when ready.`, true);
        } else {
            setMessage(ids.length ? 'Selected results deleted.' : '');
        }
    }

    refreshButton.addEventListener('click', () => refresh(true));
    autoCheckbox.addEventListener('change', () => {
        try { localStorage.setItem(AUTO_REFRESH_KEY, autoCheckbox.checked ? 'true' : 'false'); } catch (_) { /* Storage may be unavailable. */ }
        autoGeneration++;
        if (!autoCheckbox.checked && autoController) {
            autoController.abort();
            setMessage('Auto-refresh off.');
        }
    });
    bulkButton.addEventListener('click', bulkDelete);
    document.addEventListener('change', (event) => {
        if (event.target.matches('#select-all')) {
            getRowChecks().forEach((check) => { check.checked = event.target.checked; });
            updateBulkButton();
        } else if (event.target.matches('.row-check')) {
            updateBulkButton();
        }
    });

    try { autoCheckbox.checked = localStorage.getItem(AUTO_REFRESH_KEY) === 'true'; } catch (_) { autoCheckbox.checked = false; }
    updateControls();
    setInterval(() => {
        if (autoCheckbox.checked && !document.hidden && !refreshing && !deleting) refresh(false);
    }, AUTO_REFRESH_MS);

})();
