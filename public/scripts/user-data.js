import { getRequestHeaders } from '../script.js';
import { POPUP_TYPE, POPUP_RESULT, callGenericPopup } from './popup.js';
import { renderTemplateAsync } from './templates.js';
import { getCurrentUserHandle } from './user.js';
import { t } from './i18n.js';

/** @returns {Promise<{allowFullDataBackup: boolean, allowKeysExposure: boolean}>} */
async function fetchBackupCapabilities() {
    try {
        const response = await fetch('/api/users/backup/capabilities', {
            headers: getRequestHeaders({ omitContentType: true }),
        });

        if (!response.ok) {
            throw new Error('Failed to fetch backup capabilities');
        }

        return await response.json();
    } catch (error) {
        console.error('Error fetching backup capabilities:', error);
        return { allowFullDataBackup: false, allowKeysExposure: false };
    }
}

/**
 * Show the export options modal and start a native streaming download.
 * The archive is fetched via an anchor so the browser streams it to disk and the
 * whole zip is never held in memory.
 * @param {string} [targetHandle] Handle to back up (admins only). Defaults to the current user.
 * @returns {Promise<void>}
 */
export async function exportUserData(targetHandle = '') {
    try {
        const capabilities = await fetchBackupCapabilities();

        const tpl = $(await renderTemplateAsync('userDataBackup'));

        if (!capabilities.allowKeysExposure) {
            tpl.find('#userDataIncludeKeys').prop('disabled', true);
            tpl.find('.userDataKeysNote').show();
        } else {
            tpl.find('#userDataIncludeKeys').prop('disabled', false);
            tpl.find('.userDataKeysNote').hide();
        }

        const result = await callGenericPopup(tpl, POPUP_TYPE.TEXT, '', {
            okButton: t`Export`,
            cancelButton: t`Cancel`,
            wide: true,
            large: true,
        });

        if (result !== POPUP_RESULT.AFFIRMATIVE) {
            return;
        }

        const includeBackups = tpl.find('#userDataIncludeBackups').is(':checked') ? 1 : 0;
        const includeKeys = (capabilities.allowKeysExposure && tpl.find('#userDataIncludeKeys').is(':checked')) ? 1 : 0;

        const handle = targetHandle || getCurrentUserHandle();
        const now = new Date();
        const pad = (n) => String(n).padStart(2, '0');
        const filename = `${handle}-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}.zip`;

        let url = `/api/users/backup?includeKeys=${includeKeys}&includeBackups=${includeBackups}`;
        if (targetHandle) {
            url += `&handle=${encodeURIComponent(targetHandle)}`;
        }

        toastr.info(t`Starting backup download…`, t`Export`);

        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    } catch (error) {
        console.error('Error exporting user data:', error);
        toastr.error(error.message || String(error), t`Export Failed`);
    }
}

/**
 * Show the restore confirmation modal, upload the chosen archive via XHR
 * (for upload progress) and report server-side progress in a body-level overlay
 * that survives the modal closing.
 * @returns {Promise<void>}
 */
export async function restoreUserData() {
    try {
        const tpl = $(await renderTemplateAsync('userDataRestore'));

        tpl.find('#userDataRestoreFile').on('change', function () {
            const name = this.files && this.files[0] ? this.files[0].name : '';
            tpl.find('.userDataRestoreFileName').text(name || t`No file selected.`);
        });

        const result = await callGenericPopup(tpl, POPUP_TYPE.TEXT, '', {
            okButton: t`Restore`,
            cancelButton: t`Cancel`,
            wide: true,
            large: true,
        });

        if (result !== POPUP_RESULT.AFFIRMATIVE) {
            return;
        }

        const fileInput = tpl.find('#userDataRestoreFile')[0];
        const file = fileInput && fileInput.files ? fileInput.files[0] : null;
        if (!file) {
            toastr.error(t`Please choose a backup .zip file.`, t`No File Selected`);
            return;
        }
        if (!file.name.toLowerCase().endsWith('.zip')) {
            toastr.error(t`Please select a .zip file.`, t`Invalid File`);
            return;
        }

        const overlay = createRestoreOverlay();
        const state = { uploadDone: false };
        const monitor = startRestoreMonitor(overlay, state);
        overlay.phase.textContent = t`Uploading…`;
        overlay.bar.value = 0;
        overlay.status.textContent = `${file.name} (${humanFileSizeJS(file.size)})`;

        try {
            await uploadRestoreArchive(file, overlay, () => { state.uploadDone = true; });
            monitor.stop();
            overlay.phase.textContent = t`Done`;
            toastr.success(t`Backup restored successfully. Reloading…`, t`Restore Complete`);
            setTimeout(() => location.reload(), 1500);
        } catch (error) {
            monitor.stop();
            overlay.remove();
            console.error('Error restoring user data:', error);
            toastr.error(error.message || String(error), t`Restore Failed`);
        }
    } catch (error) {
        console.error('Error restoring user data:', error);
        toastr.error(error.message || String(error), t`Restore Failed`);
    }
}

/**
 * Upload the archive with XMLHttpRequest. The browser streams the File from disk
 * and it is never read into memory, so multi-GB archives are safe.
 * @param {File} file Archive to upload
 * @param {ReturnType<typeof createRestoreOverlay>} overlay Progress overlay
 * @param {() => void} onUploadComplete Called once the request body is fully sent
 * @returns {Promise<any>} Parsed server response
 */
function uploadRestoreArchive(file, overlay, onUploadComplete) {
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/users/restore');

        const headers = getRequestHeaders({ omitContentType: true });
        if (headers['X-CSRF-Token']) {
            xhr.setRequestHeader('X-CSRF-Token', headers['X-CSRF-Token']);
        }

        xhr.upload.onprogress = function (e) {
            if (e.lengthComputable) {
                const pct = Math.round((e.loaded / e.total) * 100);
                overlay.bar.value = pct;
                overlay.status.textContent = `${humanFileSizeJS(e.loaded)} / ${humanFileSizeJS(e.total)} (${pct}%)`;
            }
        };
        xhr.upload.onload = () => onUploadComplete();

        xhr.onload = function () {
            try {
                const data = JSON.parse(xhr.responseText);
                if (xhr.status >= 200 && xhr.status < 300) {
                    resolve(data);
                } else {
                    reject(new Error(data.error || `Server returned ${xhr.status}`));
                }
            } catch {
                reject(new Error(`Invalid response from server (HTTP ${xhr.status})`));
            }
        };

        xhr.onerror = function () {
            reject(new Error(t`Network error during upload.`));
        };

        const formData = new FormData();
        formData.append('file', file);
        xhr.send(formData);
    });
}

/** @returns {{phase: HTMLElement, bar: HTMLProgressElement, status: HTMLElement, remove: () => void}} */
function createRestoreOverlay() {
    const overlay = document.createElement('div');
    // Explicit viewport box: a transformed ancestor (ST applies several) turns
    // position:fixed into absolute positioning against body, and body can have
    // zero height on mobile — inset:0 then collapses the overlay.
    overlay.style.cssText = 'position:fixed;left:0;top:0;width:100vw;height:100vh;z-index:99999;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,0.6);';

    const panel = document.createElement('div');
    panel.className = 'flex-container flexFlowColumn flexGap10';
    panel.style.cssText = 'background:var(--SmartThemeBlurTintColor,#1e1e1e);padding:20px;border-radius:10px;width:min(90vw,480px);';
    panel.innerHTML = '<h3 class="userDataRestorePhase"></h3>'
        + '<progress class="userDataRestoreProgressBar" max="100" value="0" style="width:100%;"></progress>'
        + '<small class="userDataRestoreStatus"></small>';

    overlay.appendChild(panel);
    document.body.appendChild(overlay);

    return {
        phase: /** @type {HTMLElement} */ (panel.querySelector('.userDataRestorePhase')),
        bar: /** @type {HTMLProgressElement} */ (panel.querySelector('.userDataRestoreProgressBar')),
        status: /** @type {HTMLElement} */ (panel.querySelector('.userDataRestoreStatus')),
        remove: () => overlay.remove(),
    };
}

/**
 * Poll the server's restore status and drive the overlay while the upload is in flight.
 * @param {ReturnType<typeof createRestoreOverlay>} overlay Progress overlay
 * @param {{uploadDone: boolean}} state Shared flags
 * @returns {{stop: () => void}}
 */
function startRestoreMonitor(overlay, state) {
    const phaseLabels = {
        receiving: t`Receiving…`,
        validating: t`Validating archive…`,
        restoring: t`Restoring files…`,
        swapping: t`Swapping data…`,
        idle: t`Done`,
    };
    let stopped = false;

    (async () => {
        while (!stopped) {
            try {
                const response = await fetch('/api/users/restore/status', {
                    headers: getRequestHeaders({ omitContentType: true }),
                });
                if (response.ok) {
                    const status = await response.json();
                    overlay.phase.textContent = phaseLabels[status.phase] || status.phase || '';
                    if (state.uploadDone && status.active) {
                        if (status.total > 0) {
                            const pct = Math.min(100, Math.round((status.files / status.total) * 100));
                            overlay.bar.value = pct;
                            overlay.status.textContent = `${status.files} / ${status.total} files (${pct}%) — ${humanFileSizeJS(status.bytes)}`;
                        } else {
                            overlay.status.textContent = humanFileSizeJS(status.bytes);
                        }
                    }
                }
            } catch {
                // network hiccup, keep polling
            }
            await sleep(400);
        }
    })();

    return { stop() { stopped = true; } };
}

/** @param {number} ms @returns {Promise<void>} */
function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
}

/** @param {number} bytes @returns {string} */
function humanFileSizeJS(bytes) {
    if (!bytes) {
        return '0 B';
    }
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    const size = bytes / Math.pow(1024, i);
    return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
