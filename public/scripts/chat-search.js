/**
 * In-chat message search: find text across chat messages, highlight every
 * match, and jump between messages. Modeled on setting-search.js (event
 * binding + CSS-class highlighting) and the /chat-jump scroll pattern.
 *
 * Rendered messages are searched directly; older history hidden by chat
 * truncation can be pulled into the DOM with the load-all button, after
 * which search covers the whole chat.
 */

import { eventSource, event_types } from './events.js';
import { flashHighlight } from './utils.js';
// Cycle-safe: script.js imports this module, but these bindings are only
// dereferenced at runtime (turtle-buttons.js already imports ../script.js).
import { showMoreMessages, getFirstDisplayedMessageId } from '../script.js';

const BAR_ID = 'chat_search_bar';
const INPUT_ID = 'chatSearch';
const COUNT_ID = 'chatSearchCount';
const PREV_ID = 'chatSearchPrev';
const NEXT_ID = 'chatSearchNext';
const CLOSE_ID = 'chatSearchClose';
const LOAD_ALL_ID = 'chatSearchLoadAll';
const MATCH_CLASS = 'chat-search-match';
const CURRENT_CLASS = 'chat-search-current';
const LOADING_CLASS = 'chat-search-loading';
const INPUT_DEBOUNCE_MS = 150;
const MAX_LOAD_ALL_ITERATIONS = 100;

/** @type {HTMLElement[]} */
let matches = [];
let currentIndex = -1;
let debounceTimer = null;
let loadAllInProgress = false;

function getBar() {
    return document.getElementById(BAR_ID);
}

function getInput() {
    return document.getElementById(INPUT_ID);
}

function getCount() {
    return document.getElementById(COUNT_ID);
}

function getQuery() {
    const input = getInput();
    return input ? String(input.value ?? '') : '';
}

function updateCount() {
    const count = getCount();
    if (!count) {
        return;
    }
    count.textContent = matches.length === 0 ? '0/0' : `${currentIndex + 1}/${matches.length}`;
}

/**
 * Remove all <mark> wrappers created by a previous search and merge the
 * split text nodes back together.
 */
function clearChatSearchMarks() {
    const marks = document.querySelectorAll(`#chat mark.${MATCH_CLASS}`);
    /** @type {Set<ParentNode>} */
    const parents = new Set();
    marks.forEach(mark => {
        if (mark.parentNode) {
            parents.add(mark.parentNode);
        }
        const text = document.createTextNode(mark.textContent ?? '');
        mark.replaceWith(text);
    });
    parents.forEach(parent => parent.normalize());
    matches = [];
    currentIndex = -1;
    updateCount();
}

/**
 * Wrap every case-insensitive occurrence of query inside text nodes under root.
 * @param {Element} root
 * @param {string} queryLower
 */
function wrapMatchesInElement(root, queryLower) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
        acceptNode(node) {
            if (!node.nodeValue || node.nodeValue.toLowerCase().indexOf(queryLower) === -1) {
                return NodeFilter.FILTER_SKIP;
            }
            // Never search inside an existing match mark.
            if (node.parentElement && node.parentElement.closest(`mark.${MATCH_CLASS}`)) {
                return NodeFilter.FILTER_SKIP;
            }
            return NodeFilter.FILTER_ACCEPT;
        },
    });

    /** @type {Text[]} */
    const nodes = [];
    let node = walker.nextNode();
    while (node) {
        nodes.push(/** @type {Text} */ (node));
        node = walker.nextNode();
    }

    nodes.forEach(textNode => {
        const text = textNode.nodeValue ?? '';
        const lower = text.toLowerCase();
        const fragment = document.createDocumentFragment();
        let cursor = 0;
        let idx = lower.indexOf(queryLower);
        while (idx !== -1) {
            if (idx > cursor) {
                fragment.appendChild(document.createTextNode(text.slice(cursor, idx)));
            }
            const mark = document.createElement('mark');
            mark.className = MATCH_CLASS;
            mark.textContent = text.slice(idx, idx + queryLower.length);
            fragment.appendChild(mark);
            cursor = idx + queryLower.length;
            idx = lower.indexOf(queryLower, cursor);
        }
        if (cursor < text.length) {
            fragment.appendChild(document.createTextNode(text.slice(cursor)));
        }
        textNode.replaceWith(fragment);
    });
}

function collectMatches() {
    const marks = Array.from(document.querySelectorAll(`#chat .mes mark.${MATCH_CLASS}`));
    matches = /** @type {HTMLElement[]} */ (marks.filter(el => el instanceof HTMLElement));
}

function setCurrent(index, scroll) {
    document.querySelectorAll(`#chat mark.${CURRENT_CLASS}`).forEach(el => el.classList.remove(CURRENT_CLASS));
    if (matches.length === 0) {
        currentIndex = -1;
        updateCount();
        return;
    }
    currentIndex = ((index % matches.length) + matches.length) % matches.length;
    const current = matches[currentIndex];
    current.classList.add(CURRENT_CLASS);
    updateCount();
    if (scroll) {
        scrollToMatch(current);
    }
}

function scrollToMatch(mark) {
    try {
        mark.scrollIntoView({ behavior: 'smooth', block: 'center' });
    } catch (_) {
        // scrollIntoView options unsupported: fall back to instant jump.
        try {
            mark.scrollIntoView();
        } catch (_) {
            // ignore
        }
    }
    const mes = mark.closest('.mes');
    if (mes) {
        try {
            flashHighlight($(mes), 1000);
        } catch (_) {
            // highlight is best-effort
        }
    }
}

export function runChatSearch(scrollToFirst = true) {
    clearChatSearchMarks();
    const query = getQuery().trim();
    if (query === '') {
        return;
    }
    const queryLower = query.toLowerCase();
    document.querySelectorAll('#chat .mes').forEach(mes => {
        mes.querySelectorAll('.mes_text, .name_text').forEach(container => {
            wrapMatchesInElement(container, queryLower);
        });
    });
    collectMatches();
    if (matches.length > 0) {
        setCurrent(0, scrollToFirst);
    } else {
        updateCount();
    }
    updateLoadAllVisibility();
}

export function nextChatMatch() {
    if (matches.length === 0) {
        return;
    }
    setCurrent(currentIndex + 1, true);
}

export function prevChatMatch() {
    if (matches.length === 0) {
        return;
    }
    setCurrent(currentIndex - 1, true);
}

function isHistoryTruncated() {
    try {
        const first = getFirstDisplayedMessageId();
        return Number.isFinite(first) && first > 0;
    } catch (_) {
        return false;
    }
}

function updateLoadAllVisibility() {
    const button = document.getElementById(LOAD_ALL_ID);
    if (!button) {
        return;
    }
    button.classList.toggle('displayNone', !isHistoryTruncated() || loadAllInProgress);
}

function setLoadAllSpinner(spinning) {
    const button = document.getElementById(LOAD_ALL_ID);
    const icon = button?.querySelector('i');
    if (!button || !icon) {
        return;
    }
    button.classList.toggle(LOADING_CLASS, spinning);
    icon.classList.toggle('fa-angles-down', !spinning);
    icon.classList.toggle('fa-spinner', spinning);
    icon.classList.toggle('fa-spin', spinning);
}

export async function loadAllMessagesForSearch() {
    if (loadAllInProgress) {
        return;
    }
    loadAllInProgress = true;
    updateLoadAllVisibility();
    setLoadAllSpinner(true);
    try {
        let iterations = 0;
        while (isHistoryTruncated() && iterations++ < MAX_LOAD_ALL_ITERATIONS) {
            await showMoreMessages();
        }
    } finally {
        loadAllInProgress = false;
        setLoadAllSpinner(false);
    }
    runChatSearch(false);
    updateLoadAllVisibility();
}

export function openChatSearch() {
    const bar = getBar();
    const input = getInput();
    if (!bar || !input) {
        return;
    }
    bar.classList.remove('displayNone');
    input.focus();
    try {
        input.select();
    } catch (_) {
        // ignore
    }
    updateLoadAllVisibility();
    // Refresh results in case the chat changed while the bar was closed.
    if (getQuery().trim() !== '') {
        runChatSearch(false);
    }
}

export function closeChatSearch() {
    const bar = getBar();
    if (bar) {
        bar.classList.add('displayNone');
    }
    clearChatSearchMarks();
    const input = getInput();
    if (input) {
        input.value = '';
    }
}

/**
 * @param {HTMLElement | null} element
 */
function isEditable(element) {
    if (!element) {
        return false;
    }
    if (element.id === INPUT_ID) {
        return true;
    }
    const tag = (element.tagName ?? '').toUpperCase();
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') {
        return true;
    }
    return element.isContentEditable === true;
}

function scheduleSearch() {
    if (debounceTimer) {
        clearTimeout(debounceTimer);
    }
    debounceTimer = setTimeout(() => {
        runChatSearch(true);
    }, INPUT_DEBOUNCE_MS);
}

function rescanIfOpen() {
    const bar = getBar();
    if (!bar || bar.classList.contains('displayNone')) {
        return;
    }
    if (getQuery().trim() === '') {
        return;
    }
    runChatSearch(false);
}

export function initChatSearch() {
    const input = getInput();
    if (!input) {
        console.warn('WARN: #chatSearch not found, chat search disabled.');
        return;
    }

    $(`#${INPUT_ID}`).on('input', scheduleSearch);
    $(`#${NEXT_ID}`).on('click', nextChatMatch);
    $(`#${PREV_ID}`).on('click', prevChatMatch);
    $(`#${CLOSE_ID}`).on('click', closeChatSearch);
    $(`#${LOAD_ALL_ID}`).on('click', loadAllMessagesForSearch);

    input.addEventListener('keydown', event => {
        if (event.key === 'Enter') {
            event.preventDefault();
            if (event.shiftKey) {
                prevChatMatch();
            } else {
                nextChatMatch();
            }
        } else if (event.key === 'Escape') {
            event.preventDefault();
            closeChatSearch();
        }
    });

    // Esc closes the bar whenever focus sits inside it (input or nav buttons),
    // mirroring browser find bars. Popups steal focus when open, so this does
    // not fight ST dialog Esc handling.
    document.addEventListener('keydown', event => {
        if (event.key !== 'Escape') {
            return;
        }
        const bar = getBar();
        if (!bar || bar.classList.contains('displayNone')) {
            return;
        }
        if (!bar.contains(/** @type {Node} */ (document.activeElement))) {
            return;
        }
        event.preventDefault();
        closeChatSearch();
    });

    // The nav buttons are divs: make them keyboard-operable.
    ['chatSearchPrev', 'chatSearchNext', 'chatSearchClose', 'chatSearchLoadAll'].forEach(id => {
        const el = document.getElementById(id);
        if (!el) {
            return;
        }
        el.addEventListener('keydown', event => {
            if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                el.click();
            }
        });
    });

    // Desktop shortcut: Ctrl/Cmd+F opens the chat search instead of the
    // browser find. Never hijack typing inside other inputs.
    document.addEventListener('keydown', event => {
        const isFind = (event.ctrlKey || event.metaKey) && !event.shiftKey && !event.altKey
            && String(event.key ?? '').toLowerCase() === 'f';
        if (!isFind) {
            return;
        }
        if (isEditable(/** @type {HTMLElement} */ (document.activeElement)) && document.activeElement?.id !== INPUT_ID) {
            return;
        }
        event.preventDefault();
        openChatSearch();
    });

    try {
        if (eventSource && event_types) {
            if (event_types.CHAT_CHANGED) {
                eventSource.on(event_types.CHAT_CHANGED, () => {
                    clearChatSearchMarks();
                    updateLoadAllVisibility();
                });
            }
            if (event_types.MESSAGE_RECEIVED) {
                eventSource.on(event_types.MESSAGE_RECEIVED, rescanIfOpen);
            }
            if (event_types.MESSAGE_UPDATED) {
                eventSource.on(event_types.MESSAGE_UPDATED, rescanIfOpen);
            }
            if (event_types.MORE_MESSAGES_LOADED) {
                eventSource.on(event_types.MORE_MESSAGES_LOADED, () => {
                    rescanIfOpen();
                    updateLoadAllVisibility();
                });
            }
        }
    } catch (_) {
        // event wiring is best-effort
    }

    // Hook for automated verification.
    globalThis.__ttChatSearch = {
        open: openChatSearch,
        close: closeChatSearch,
        search: runChatSearch,
        next: nextChatMatch,
        prev: prevChatMatch,
        loadAll: loadAllMessagesForSearch,
        isTruncated: isHistoryTruncated,
        state: () => ({ count: matches.length, index: currentIndex, query: getQuery() }),
    };
}
