import { getRequestHeaders } from '../script.js';
import { renderTemplateAsync } from './templates.js';
import { POPUP_TYPE, POPUP_RESULT, callGenericPopup } from './popup.js';
import { t } from './i18n.js';
import { canViewSecrets, findSecret, readSecretState, writeSecret, deleteSecret } from './secrets.js';

const SECRET_KEY_URL = 'backupper_url';
const SECRET_KEY_DEVICE = 'api_key_backupper';

let statusPollTimer = null;

function humanFileSize(bytes) {
    if (!bytes) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatDateTime(iso) {
    if (!iso) return '\u2014';
    try { return new Date(iso).toLocaleString(); } catch { return iso; }
}

function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

function buildSnapshotData(snapshots) {
    return (snapshots || []).map(s => {
        const tooltip = [
            s.device ? `Device: ${s.device}` : '',
            s.logicalBytes ? `Logical: ${humanFileSize(s.logicalBytes)}` : '',
            s.storedBytes ? `Stored: ${humanFileSize(s.storedBytes)}` : '',
            s.sharedBytes ? `Shared: ${humanFileSize(s.sharedBytes)}` : '',
            s.appVersion ? `Version: ${s.appVersion}` : '',
        ].filter(Boolean).join('\n');
        return {
            id: s.id,
            receivedAtFormatted: formatDateTime(s.receivedAt),
            labelOrDefault: s.label || '\u2014',
            fileCountOrDefault: s.fileCount || 0,
            bytesLine: s.logicalBytes
                ? `${humanFileSize(s.logicalBytes)} / ${humanFileSize(s.storedBytes || 0)}`
                : '',
            tooltip,
        };
    });
}

function buildLastSyncText(status) {
    if (!status.lastSyncAt) return '';
    const sync = status.lastSync || {};
    const parts = [formatDateTime(status.lastSyncAt)];
    if (sync.scanned !== undefined) parts.push(`${t`Scanned`}: ${sync.scanned}`);
    if (sync.uploaded !== undefined && sync.uploaded > 0) {
        parts.push(`${t`Uploaded`}: ${sync.uploaded} (${humanFileSize(sync.uploadedBytes || 0)})`);
    }
    if (sync.duplicate) parts.push(t`Unchanged`);
    return parts.join(' \u2014 ');
}

async function fetchRemoteStatus() {
    try {
        const response = await fetch('/api/remote/status', {
            headers: getRequestHeaders({ omitContentType: true }),
        });
        if (!response.ok) return null;
        return await response.json();
    } catch { return null; }
}

async function fetchSnapshots() {
    try {
        const response = await fetch('/api/remote/snapshots', {
            headers: getRequestHeaders({ omitContentType: true }),
        });
        if (!response.ok) return [];
        const data = await response.json();
        return data.snapshots || [];
    } catch { return []; }
}

async function startSync(label, includeKeys, includeBackups) {
    try {
        const response = await fetch('/api/remote/sync', {
            method: 'POST',
            headers: getRequestHeaders(),
            body: JSON.stringify({ label: label || undefined, includeKeys, includeBackups }),
        });
        if (!response.ok) {
            const data = await response.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${response.status}`);
        }
        return await response.json();
    } catch (error) {
        toastr.error(error.message || String(error), t`Sync Failed`);
        return null;
    }
}

async function startRestore(id, confirmText, skipPreSnapshot) {
    try {
        const body = { id, confirm: confirmText };
        if (skipPreSnapshot) body.skipPreSnapshot = true;
        const response = await fetch('/api/remote/restore', {
            method: 'POST',
            headers: getRequestHeaders(),
            body: JSON.stringify(body),
        });
        if (!response.ok) {
            const data = await response.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${response.status}`);
        }
        return await response.json();
    } catch (error) {
        toastr.error(error.message || String(error), t`Restore Failed`);
        return null;
    }
}

async function createRemoteOverlay(operationType) {
    const tpl = $(await renderTemplateAsync('remoteBackupProgress', {
        phase: operationType === 'sync' ? t`Starting sync\u2026` : t`Preparing restore\u2026`,
        status: '',
    }));
    document.body.appendChild(tpl[0]);

    return {
        element: tpl[0],
        phase: tpl.find('.remoteBackupProgressPhase')[0],
        bar: tpl.find('.remoteBackupProgress progress')[0],
        status: tpl.find('.remoteBackupProgressStatus')[0],
        remove() { tpl.remove(); },
    };
}

function removeRemoteOverlay() {
    document.querySelectorAll('.remoteBackupOverlay').forEach(el => el.remove());
}

function stopStatusPoll() {
    if (statusPollTimer) {
        clearTimeout(statusPollTimer);
        statusPollTimer = null;
    }
}

/**
 * Popups are <dialog> elements opened with showModal(): they sit in the browser's
 * top layer and paint above ANY z-index, so a body-level progress overlay would be
 * hidden behind them. The rebuild-index flow has the same constraint and works
 * because its confirmation dialog is dismissed before the overlay appears - do the
 * same here. It also means the modal is re-opened exactly once afterwards.
 */
function closeOpenPopups() {
    document.querySelectorAll('dialog[open]').forEach(dialog => {
        try { dialog.close(); } catch { /* already closed */ }
    });
}

async function fetchRemoteStatusQuiet() {
    try {
        const response = await fetch('/api/remote/status', {
            headers: getRequestHeaders({ omitContentType: true }),
        });
        if (!response.ok) return null;
        return await response.json();
    } catch { return null; }
}

function startRemoteStatusPoll(overlay, operationType) {
    stopStatusPoll();

    return new Promise(resolve => {
        async function poll() {
            const status = await fetchRemoteStatusQuiet();
            if (!status) {
                stopStatusPoll();
                resolve({ success: false, error: t`Lost connection to server.` });
                return;
            }
            if (status.busy) {
                updateOverlayProgress(overlay, status, operationType);
                statusPollTimer = setTimeout(poll, 1200);
                return;
            }
            stopStatusPoll();
            resolve({ success: !status.lastError, error: status.lastError || '', status });
        }
        statusPollTimer = setTimeout(poll, 800);
    });
}

function updateOverlayProgress(overlay, status, operationType) {
    const p = status.progress || {};
    const phase = status.phase || '';
    overlay.phase.textContent = humanPhaseLabel(phase, p, operationType);

    if (p.total > 0) {
        overlay.bar.setAttribute('max', '100');
        overlay.bar.value = Math.min(100, Math.round((p.files / p.total) * 100));
        overlay.status.textContent = `${p.files} / ${p.total} files \u2014 ${humanFileSize(p.bytes)}`;
    } else {
        overlay.bar.removeAttribute('value');
        overlay.status.textContent = humanFileSize(p.bytes);
    }
}

function humanPhaseLabel(phase, progress, operationType) {
    if (operationType === 'sync') {
        const syncLabels = {
            starting: t`Starting sync\u2026`,
            scanning: t`Scanning your files\u2026`,
            checking: progress && progress.total > 0
                ? t`Checking which files are new (${progress.files}/${progress.total})\u2026`
                : t`Checking which files are new\u2026`,
            uploading: progress && progress.total > 0
                ? t`Uploading ${progress.files}/${progress.total} files \u2014 ${humanFileSize(progress.bytes)}`
                : t`Uploading files\u2026`,
            committing: t`Saving snapshot\u2026`,
        };
        return syncLabels[phase] || phase || t`Working\u2026`;
    }
    const restoreLabels = {
        'pre-snapshot': t`Taking a safety snapshot\u2026`,
        staging: progress && progress.total > 0
            ? t`Restoring file ${progress.files}/${progress.total}\u2026`
            : t`Restoring files\u2026`,
        swapping: t`Activating the restored data\u2026`,
        starting: t`Starting restore\u2026`,
    };
    return restoreLabels[phase] || phase || t`Working\u2026`;
}

/**
 * Close any remote-backup modal that is already open. Every button handler in the
 * modal re-opens it (to refresh), so without this each click stacks another popup.
 * Only the topmost dialog containing this feature is closed, so the profile popup
 * that launched it stays open.
 */
function closeExistingRemoteModals() {
    const open = [...document.querySelectorAll('dialog[open]')]
        .filter(dialog => dialog.querySelector('.remoteBackupSyncButton, .remoteBackupSaveButton'));
    const topmost = open[open.length - 1];
    if (topmost) {
        try { topmost.close(); } catch { /* already closed */ }
    }
}

export async function openRemoteBackupModal() {
    closeExistingRemoteModals();

    const status = await fetchRemoteStatus();
    if (!status || !status.available) {
        toastr.info(t`Remote backup is not enabled on this server.`, t`Remote Backup`);
        return;
    }

    let allowKeysExposure = false;
    try { allowKeysExposure = await canViewSecrets() === true; } catch { /* ignore */ }

    const data = { configured: status.configured };

    if (status.configured) {
        data.url = status.url || '';
        data.reachable = status.reachable;
        data.allowKeysExposure = allowKeysExposure;
        data.lastSyncAt = status.lastSyncAt;
        data.lastSyncText = buildLastSyncText(status);
        data.lastError = status.lastError || '';
        data.today = new Date().toISOString().slice(0, 10);
        const snapshots = await fetchSnapshots();
        data.snapshots = buildSnapshotData(snapshots);
    } else {
        data.savedUrl = '';
        try {
            const savedUrl = await findSecret(SECRET_KEY_URL);
            if (savedUrl) data.savedUrl = savedUrl;
        } catch { /* ignore */ }
    }

    const tpl = $(await renderTemplateAsync('remoteBackup', data));

    if (data.configured) {
        tpl.find('.remoteBackupSyncButton').on('click', async function () {
            const $btn = $(this);
            $btn.css({ 'pointer-events': 'none', opacity: '0.5' });

            const label = tpl.find('#remoteSyncLabel').val()?.toString().trim() || '';
            const includeBackups = tpl.find('#remoteSyncIncludeBackups').is(':checked');
            const includeKeys = tpl.find('#remoteSyncIncludeKeys').is(':checked');

            const result = await startSync(label, includeKeys, includeBackups);
            if (!result || !result.started) {
                $btn.css({ 'pointer-events': '', opacity: '' });
                return;
            }

            closeOpenPopups();
            const overlay = await createRemoteOverlay('sync');
            try {
                const outcome = await startRemoteStatusPoll(overlay, 'sync');
                removeRemoteOverlay();
                if (!outcome.success) {
                    const isSafetyError = /safety|pre.?snapshot|preSnapshot/i.test(outcome.error);
                    if (isSafetyError && outcome.status?.lastSnapshotId) {
                        await offerSkipPreSnapshot(outcome.error, outcome.status.lastSnapshotId);
                    } else {
                        toastr.error(outcome.error, t`Sync Error`);
                    }
                } else {
                    toastr.success(t`Sync complete.`, t`Remote Sync`);
                }
            } catch {
                removeRemoteOverlay();
            }
            await openRemoteBackupModal();
        });

        tpl.find('.remoteBackupRefreshButton').on('click', async () => {
            await openRemoteBackupModal();
        });

        tpl.find('.remoteBackupDisconnectButton').on('click', async () => {
            const confirmed = await callGenericPopup(
                t`Disconnect Remote Backup?`,
                POPUP_TYPE.CONFIRM,
                t`This will remove the saved URL and device key. Snapshots on the server are not affected.`,
                { okButton: t`Disconnect`, cancelButton: t`Cancel` },
            );
            if (confirmed !== POPUP_RESULT.AFFIRMATIVE) return;
            await deleteSecret(SECRET_KEY_URL);
            await deleteSecret(SECRET_KEY_DEVICE);
            await readSecretState();
            toastr.success(t`Remote backup disconnected.`);
            await openRemoteBackupModal();
        });

        tpl.find('.remoteBackupRestoreButton').on('click', async function () {
            const snapshotId = $(this).data('snapshot-id');
            await handleRestore(snapshotId);
        });

        await callGenericPopup(tpl, POPUP_TYPE.TEXT, '', {
            okButton: t`Close`,
            cancelButton: t`Cancel`,
            wide: true,
            large: true,
        });
    } else {
        tpl.find('.remoteBackupSaveButton').on('click', async () => {
            const url = tpl.find('#remoteBackupUrl').val()?.toString().trim();
            const key = tpl.find('#remoteBackupKey').val()?.toString().trim();
            if (!url) { toastr.error(t`Please enter a Backupper URL.`, t`Missing URL`); return; }
            if (!key) { toastr.error(t`Please enter a device key.`, t`Missing Key`); return; }
            try { new URL(url); } catch {
                toastr.error(t`Please enter a valid URL (e.g. https://vault.example.com).`, t`Invalid URL`);
                return;
            }
            const $btn = tpl.find('.remoteBackupSaveButton');
            $btn.css({ 'pointer-events': 'none', opacity: '0.5' });
            try {
                await writeSecret(SECRET_KEY_URL, url, 'Backupper URL');
                await writeSecret(SECRET_KEY_DEVICE, key, 'Backupper Device Key');
                await readSecretState();
                toastr.success(t`Remote backup configured.`, t`Saved`);
                await openRemoteBackupModal();
            } catch (error) {
                toastr.error(error.message || String(error), t`Save Failed`);
            } finally {
                $btn.css({ 'pointer-events': '', opacity: '' });
            }
        });

        await callGenericPopup(tpl, POPUP_TYPE.TEXT, '', {
            okButton: t`Close`,
            cancelButton: t`Cancel`,
            wide: true,
            large: true,
        });
    }

    async function handleRestore(snapshotId) {
        const restoreTpl = $(await renderTemplateAsync('remoteBackupRestore', { snapshotId }));
        const result = await callGenericPopup(restoreTpl, POPUP_TYPE.TEXT, '', {
            okButton: t`Restore`,
            cancelButton: t`Cancel`,
            wide: true,
            large: true,
        });
        if (result !== POPUP_RESULT.AFFIRMATIVE) return;

        const typed = restoreTpl.find('#remoteRestoreConfirm').val()?.toString().trim();
        if (typed !== snapshotId) {
            toastr.error(t`Snapshot ID does not match.`, t`Restore Cancelled`);
            return;
        }

        const restoreResult = await startRestore(snapshotId, snapshotId, false);
        if (!restoreResult || !restoreResult.started) return;

        closeOpenPopups();
        const overlay = await createRemoteOverlay('restore');
        try {
            const outcome = await startRemoteStatusPoll(overlay, 'restore');
            removeRemoteOverlay();
            if (!outcome.success) {
                const isSafetyError = /safety|pre.?snapshot|preSnapshot/i.test(outcome.error);
                if (isSafetyError && outcome.status?.lastSnapshotId) {
                    await offerSkipPreSnapshot(outcome.error, outcome.status.lastSnapshotId);
                } else {
                    toastr.error(outcome.error, t`Restore Error`);
                }
            } else {
                toastr.success(t`Restore complete. Reloading\u2026`, t`Remote Restore`);
                setTimeout(() => location.reload(), 1500);
            }
        } catch {
            removeRemoteOverlay();
        }
    }

    async function offerSkipPreSnapshot(errorMsg, snapshotId) {
        const skipTpl = $(await renderTemplateAsync('remoteBackupSkipRestore'));
        const result = await callGenericPopup(skipTpl, POPUP_TYPE.TEXT, '', {
            okButton: t`Restore Without Safety`,
            cancelButton: t`Cancel`,
            wide: true,
            large: true,
        });
        if (result !== POPUP_RESULT.AFFIRMATIVE) return;

        const typed = skipTpl.find('#remoteRestoreSkipConfirm').val()?.toString().trim();
        if (typed !== snapshotId) {
            toastr.error(t`Snapshot ID does not match.`, t`Restore Cancelled`);
            return;
        }

        const restoreResult = await startRestore(snapshotId, snapshotId, true);
        if (!restoreResult || !restoreResult.started) return;

        closeOpenPopups();
        const overlay = await createRemoteOverlay('restore');
        try {
            const outcome = await startRemoteStatusPoll(overlay, 'restore');
            removeRemoteOverlay();
            if (!outcome.success) {
                toastr.error(outcome.error, t`Restore Error`);
            } else {
                toastr.success(t`Restore complete. Reloading\u2026`, t`Remote Restore`);
                setTimeout(() => location.reload(), 1500);
            }
        } catch {
            removeRemoteOverlay();
        }
    }
}
