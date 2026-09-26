/**
 * Field search: Chrome-find-style search across character editor textareas
 * (definitions) or the alternate-greetings popup. Textareas cannot contain
 * markup, so matches are painted on a backdrop div behind a transparent
 * textarea (mirrored text + scroll position), cleared on edit or close.
 */

const BAR_ID = 'field_search_bar';
const INPUT_ID = 'fieldSearch';
const COUNT_ID = 'fieldSearchCount';
const SCOPE_ID = 'fieldSearchScope';
const PREV_ID = 'fieldSearchPrev';
const NEXT_ID = 'fieldSearchNext';
const CLOSE_ID = 'fieldSearchClose';
const CARD_BUTTON_ID = 'cardSearch';
const GREET_BUTTON_CLASS = 'field_search_greetings_btn';
const BACKDROP_CLASS = 'fs-backdrop';
const CONTENT_CLASS = 'fs-content';
const MATCH_CLASS = 'fs-match';
const CURRENT_CLASS = 'fs-current';
const ACTIVE_CLASS = 'fs-active';
const INPUT_DEBOUNCE_MS = 150;

const DEFINITION_SELECTORS = [
    '#description_textarea',
    '#firstmessage_textarea',
    '#personality_textarea',
    '#scenario_pole',
    '#mes_example_textarea',
    '#system_prompt_textarea',
    '#creator_notes_textarea',
    '#post_history_instructions_textarea',
];

const FONT_PROPS = [
    'fontStyle', 'fontVariant', 'fontWeight', 'fontSize', 'lineHeight',
    'fontFamily', 'letterSpacing', 'textIndent', 'textTransform',
    'textAlign', 'direction',
    'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft',
    'borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth',
    'whiteSpace', 'wordWrap', 'overflowWrap', 'wordBreak', 'tabSize',
];

/** @type {{ta: HTMLTextAreaElement, start: number, end: number}[]} */
let matches = [];
let currentIndex = -1;
/** @type {(() => HTMLTextAreaElement[]) | null} */
let scopeResolver = null;
let scopeLabel = '';
let debounceTimer = null;
let activeTa = null;
let backdropEl = null;
let contentEl = null;
let savedInline = null;
let resizeObserver = null;

function getBar() {
    return document.getElementById(BAR_ID);
}

function getInput() {
    return document.getElementById(INPUT_ID);
}

function getQuery() {
    const input = getInput();
    return input ? String(input.value ?? '') : '';
}

function updateCount() {
    const count = document.getElementById(COUNT_ID);
    if (count) {
        count.textContent = matches.length === 0 ? '0/0' : `${currentIndex + 1}/${matches.length}`;
    }
}

function escapeHtml(text) {
    return text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}

function resolveScope() {
    if (!scopeResolver) {
        return [];
    }
    try {
        return scopeResolver().filter(el =>
            el instanceof HTMLTextAreaElement && el.isConnected && el.offsetParent !== null);
    } catch (_) {
        return [];
    }
}

function teardownBackdrop() {
    if (backdropEl && backdropEl.parentNode) {
        backdropEl.parentNode.removeChild(backdropEl);
    }
    backdropEl = null;
    contentEl = null;
    if (resizeObserver) {
        try {
            resizeObserver.disconnect();
        } catch (_) {
            // ignore
        }
        resizeObserver = null;
    }
    if (activeTa) {
        try {
            activeTa.removeEventListener('scroll', syncScroll);
            activeTa.removeEventListener('input', onFieldEdit);
            if (savedInline) {
                activeTa.style.backgroundColor = savedInline.backgroundColor;
                activeTa.style.color = savedInline.color;
                activeTa.style.caretColor = savedInline.caretColor;
                activeTa.style.textShadow = savedInline.textShadow;
            }
            activeTa.classList.remove(ACTIVE_CLASS);
        } catch (_) {
            // ignore
        }
        activeTa = null;
        savedInline = null;
    }
}

function clearResults() {
    teardownBackdrop();
    matches = [];
    currentIndex = -1;
    updateCount();
}

function buildContentHtml(text, marks, currentMark) {
    const parts = [];
    let cursor = 0;
    marks.forEach((m, i) => {
        if (m.start > cursor) {
            parts.push(escapeHtml(text.slice(cursor, m.start)));
        }
        parts.push(`<mark class="${MATCH_CLASS}${i === currentMark ? ` ${CURRENT_CLASS}` : ''}">` +
            `${escapeHtml(text.slice(m.start, m.end))}</mark>`);
        cursor = m.end;
    });
    if (cursor < text.length) {
        parts.push(escapeHtml(text.slice(cursor)));
    }
    parts.push('\n');
    return parts.join('');
}

function syncScroll() {
    if (!activeTa || !contentEl) {
        return;
    }
    const x = Math.round(activeTa.scrollLeft);
    const y = Math.round(activeTa.scrollTop);
    contentEl.style.transform = `translate(${-x}px, ${-y}px)`;
}

function reposition() {
    if (!activeTa || !backdropEl) {
        return;
    }
    const ta = activeTa;
    const computed = window.getComputedStyle(ta);
    const rect = ta.getBoundingClientRect();
    const borderLeft = parseFloat(computed.borderLeftWidth) || 0;
    const borderTop = parseFloat(computed.borderTopWidth) || 0;
    const padLeft = parseFloat(computed.paddingLeft) || 0;
    const padRight = parseFloat(computed.paddingRight) || 0;
    const padTop = parseFloat(computed.paddingTop) || 0;

    backdropEl.style.left = `${rect.left}px`;
    backdropEl.style.top = `${rect.top}px`;
    backdropEl.style.width = `${ta.offsetWidth}px`;
    backdropEl.style.height = `${ta.offsetHeight}px`;

    if (contentEl) {
        // clientWidth excludes the vertical scrollbar, so this matches the
        // textarea's text-wrapping width exactly.
        const contentWidth = Math.max(0, ta.clientWidth - padLeft - padRight);
        contentEl.style.left = `${borderLeft + padLeft}px`;
        contentEl.style.top = `${borderTop + padTop}px`;
        contentEl.style.width = `${contentWidth}px`;
    }
    syncScroll();
}

function attachBackdrop(ta) {
    if (activeTa === ta && backdropEl) {
        reposition();
        return;
    }
    teardownBackdrop();
    const computed = window.getComputedStyle(ta);
    savedInline = {
        backgroundColor: ta.style.backgroundColor,
        color: ta.style.color,
        caretColor: ta.style.caretColor,
        textShadow: ta.style.textShadow,
    };

    backdropEl = document.createElement('div');
    backdropEl.className = BACKDROP_CLASS;
    backdropEl.setAttribute('aria-hidden', 'true');
    contentEl = document.createElement('div');
    contentEl.className = CONTENT_CLASS;
    backdropEl.appendChild(contentEl);
    FONT_PROPS.forEach(prop => {
        try {
            if (prop.startsWith('padding') || prop.startsWith('border')) {
                return;
            }
            contentEl.style[prop] = computed[prop];
        } catch (_) {
            // ignore unsupported props
        }
    });
    backdropEl.style.backgroundColor = computed.backgroundColor;
    contentEl.style.color = 'transparent';
    contentEl.style.whiteSpace = 'pre-wrap';
    contentEl.style.wordWrap = 'break-word';
    contentEl.style.overflowWrap = 'break-word';
    contentEl.style.margin = '0';
    contentEl.style.padding = '0';
    contentEl.style.border = '0';

    ta.parentNode.insertBefore(backdropEl, ta);
    ta.classList.add(ACTIVE_CLASS);
    ta.style.backgroundColor = 'transparent';
    ta.style.textShadow = 'none';

    activeTa = ta;
    ta.addEventListener('scroll', syncScroll);
    ta.addEventListener('input', onFieldEdit);
    try {
        resizeObserver = new ResizeObserver(() => reposition());
        resizeObserver.observe(ta);
    } catch (_) {
        // ignore
    }
    reposition();
}

function onFieldEdit() {
    clearResults();
}

function paintField(ta, fieldMatches, currentGlobal) {
    attachBackdrop(ta);
    const currentLocal = fieldMatches.indexOf(currentGlobal);
    contentEl.innerHTML = buildContentHtml(ta.value, fieldMatches.map(m => ({ start: m.start, end: m.end })), currentLocal);
    reposition();
    return contentEl.querySelector(`.${CURRENT_CLASS}`);
}

function setCurrent(index, scroll) {
    const live = matches.filter(m => m.ta.isConnected && m.ta.offsetParent !== null);
    if (live.length !== matches.length) {
        matches = live;
    }
    if (matches.length === 0) {
        currentIndex = -1;
        teardownBackdrop();
        updateCount();
        return;
    }
    currentIndex = ((index % matches.length) + matches.length) % matches.length;
    updateCount();
    if (!scroll) {
        return;
    }
    const match = matches[currentIndex];
    try {
        match.ta.scrollIntoView({ behavior: 'smooth', block: 'center' });
    } catch (_) {
        // ignore
    }
    window.requestAnimationFrame(() => {
        const fieldMatches = matches.filter(m => m.ta === match.ta);
        const mark = paintField(match.ta, fieldMatches, match);
        if (mark) {
            const ta = match.ta;
            const top = mark.offsetTop - ta.clientHeight / 2;
            ta.scrollTop = Math.max(0, top);
            syncScroll();
        }
    });
}

export function runFieldSearch(scrollToFirst = true) {
    teardownBackdrop();
    matches = [];
    currentIndex = -1;
    const query = getQuery().trim().toLowerCase();
    if (query !== '') {
        resolveScope().forEach(ta => {
            const text = ta.value ?? '';
            const lower = text.toLowerCase();
            let from = 0;
            let idx = lower.indexOf(query, from);
            while (idx !== -1) {
                matches.push({ ta, start: idx, end: idx + query.length });
                from = idx + query.length;
                idx = lower.indexOf(query, from);
            }
        });
    }
    if (matches.length > 0) {
        setCurrent(0, scrollToFirst);
    } else {
        updateCount();
    }
}

export function nextFieldMatch() {
    if (matches.length === 0) {
        return;
    }
    setCurrent(currentIndex + 1, true);
}

export function prevFieldMatch() {
    if (matches.length === 0) {
        return;
    }
    setCurrent(currentIndex - 1, true);
}

export function openFieldSearch(resolver, label, mountEl) {
    scopeResolver = resolver;
    scopeLabel = label;
    const bar = ensureBar();
    const input = getInput();
    if (!bar || !input) {
        return;
    }
    const target = mountEl instanceof HTMLElement ? mountEl : document.body;
    if (bar.parentElement !== target) {
        target.appendChild(bar);
    }
    bar.classList.toggle('fs-in-popup', target !== document.body);
    const scopeEl = document.getElementById(SCOPE_ID);
    if (scopeEl) {
        scopeEl.textContent = label;
    }
    bar.classList.remove('displayNone');
    input.focus();
    try {
        input.select();
    } catch (_) {
        // ignore
    }
    if (getQuery().trim() !== '') {
        runFieldSearch(false);
    }
}

export function openDefinitionSearch() {
    openFieldSearch(() => DEFINITION_SELECTORS
        .flatMap(sel => Array.from(document.querySelectorAll(sel)))
        .filter(el => el instanceof HTMLTextAreaElement), 'Definitions');
}

export function openGreetingsSearch(root) {
    const list = root instanceof HTMLElement ? root : document;
    const dialog = list instanceof Element
        ? (list.closest('dialog.popup') || list.closest('.popup'))
        : null;
    openFieldSearch(() => Array.from(list.querySelectorAll('textarea.alternate_greeting_text'))
        .filter(el => el instanceof HTMLTextAreaElement), 'Greetings', dialog);
}

export function closeFieldSearch() {
    const bar = getBar();
    if (bar) {
        bar.classList.add('displayNone');
    }
    const input = getInput();
    if (input) {
        input.value = '';
    }
    scopeResolver = null;
    clearResults();
}

function scheduleSearch() {
    if (debounceTimer) {
        clearTimeout(debounceTimer);
    }
    debounceTimer = setTimeout(() => {
        runFieldSearch(true);
    }, INPUT_DEBOUNCE_MS);
}

function ensureBar() {
    let bar = document.getElementById(BAR_ID);
    if (bar) {
        return bar;
    }
    bar = document.createElement('div');
    bar.id = BAR_ID;
    bar.className = 'displayNone';
    bar.innerHTML =
        `<span id="${SCOPE_ID}"></span>` +
        `<input id="${INPUT_ID}" type="text" placeholder="Find..." autocomplete="off">` +
        `<span id="${COUNT_ID}">0/0</span>` +
        `<div id="${PREV_ID}" class="menu_button" role="button" tabindex="0" title="Previous match"><i class="fa-solid fa-chevron-up"></i></div>` +
        `<div id="${NEXT_ID}" class="menu_button" role="button" tabindex="0" title="Next match"><i class="fa-solid fa-chevron-down"></i></div>` +
        `<div id="${CLOSE_ID}" class="menu_button" role="button" tabindex="0" title="Close search"><i class="fa-solid fa-times"></i></div>`;
    document.body.appendChild(bar);
    bindBar(bar);
    return bar;
}

function bindBar(bar) {
    if (bar.dataset.fsBound === '1') {
        return;
    }
    bar.dataset.fsBound = '1';
    const input = bar.querySelector(`#${INPUT_ID}`);
    if (!input) {
        return;
    }
    input.addEventListener('input', scheduleSearch);
    input.addEventListener('keydown', event => {
        if (event.key === 'Enter') {
            event.preventDefault();
            if (event.shiftKey) {
                prevFieldMatch();
            } else {
                nextFieldMatch();
            }
        } else if (event.key === 'Escape') {
            event.preventDefault();
            closeFieldSearch();
        }
    });
    const actions = {
        [NEXT_ID]: nextFieldMatch,
        [PREV_ID]: prevFieldMatch,
        [CLOSE_ID]: closeFieldSearch,
    };
    Object.entries(actions).forEach(([id, handler]) => {
        const el = bar.querySelector(`#${id}`);
        if (!el) {
            return;
        }
        el.addEventListener('click', handler);
        el.addEventListener('keydown', event => {
            if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                el.click();
            }
        });
    });
}

function injectGreetingsButton(popupRoot) {
    if (!(popupRoot instanceof HTMLElement)) {
        return;
    }
    if (popupRoot.closest('.template_element')) {
        return;
    }
    if (popupRoot.querySelector(`.${GREET_BUTTON_CLASS}`)) {
        return;
    }
    const anchor = popupRoot.querySelector('.add_alternate_greeting');
    if (!anchor) {
        return;
    }
    const btn = document.createElement('div');
    btn.className = `menu_button menu_button_icon ${GREET_BUTTON_CLASS}`;
    btn.setAttribute('role', 'button');
    btn.setAttribute('tabindex', '0');
    btn.title = 'Search alternate greetings';
    btn.innerHTML = '<i class="fa-solid fa-magnifying-glass"></i><span>Search</span>';
    const fire = event => {
        event.preventDefault();
        event.stopPropagation();
        openGreetingsSearch(popupRoot);
    };
    btn.addEventListener('click', fire);
    btn.addEventListener('keydown', event => {
        if (event.key === 'Enter' || event.key === ' ') {
            fire(event);
        }
    });
    anchor.after(btn);
}

function watchGreetingsPopups() {
    const observer = new MutationObserver(mutations => {
        let greetingsOpen = false;
        mutations.forEach(mutation => {
            mutation.addedNodes.forEach(node => {
                if (!(node instanceof HTMLElement)) {
                    return;
                }
                if (node.matches && node.matches('.alternate_grettings')) {
                    injectGreetingsButton(node);
                }
                (node.querySelectorAll ? node.querySelectorAll('.alternate_grettings') : []).forEach(el => {
                    injectGreetingsButton(el);
                });
            });
        });
        document.querySelectorAll('.alternate_grettings').forEach(el => {
            if (!el.closest('.template_element') && el.isConnected) {
                greetingsOpen = true;
            }
        });
        if (!greetingsOpen && scopeLabel === 'Greetings') {
            closeFieldSearch();
        }
    });
    observer.observe(document.body, { childList: true, subtree: true });
}

export function initFieldSearch() {
    ensureBar();
    if (!getInput()) {
        return;
    }

    $(`#${CARD_BUTTON_ID}`).on('click', openDefinitionSearch);

    document.addEventListener('keydown', event => {
        if (event.key !== 'Escape') {
            return;
        }
        const bar = getBar();
        if (!bar || bar.classList.contains('displayNone')) {
            return;
        }
        if (!bar.contains(document.activeElement)) {
            return;
        }
        event.preventDefault();
        closeFieldSearch();
    });

    document.addEventListener('scroll', () => {
        if (activeTa && backdropEl) {
            reposition();
        }
    }, true);

    watchGreetingsPopups();

    globalThis.__ttFieldSearch = {
        openDefinitions: openDefinitionSearch,
        openGreetings: openGreetingsSearch,
        close: closeFieldSearch,
        next: nextFieldMatch,
        prev: prevFieldMatch,
        state: () => ({ count: matches.length, index: currentIndex, query: getQuery(), scope: scopeLabel }),
    };
}
