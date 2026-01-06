/* ============================================================
   workspace.js – Core UI & interaction layer
   ------------------------------------------------------------
   This module owns the UI state, the form‑library UI, article
   creation / nesting, live field persistence, keyword‑based ads,
   context export / import, search‑filter, and the overall
   initialization routine.  All other concerns (auth, crypto,
   sync, utilities, config) are imported from their own modules.
   ============================================================ */

import { CONFIG } from './config.js';
import { toast, showSpinner, hideSpinner } from './utils.js';
import { login,logout, authState } from './auth.js';
import { encryptData, decryptData } from './crypto.js';
import { scheduleSync, downloadEncryptedContext, pushCurrentState } from './sync.js';
import { debounce } from './utils.js';

/* ---------------------------------------------------------
   Global UI state (exported for other modules)
--------------------------------------------------------- */
const uiState = {
    library: [],                     // Form‑library templates
    articleSeq: 0,                   // Incremental ID for <article>
    fieldValueMap: loadFieldValues(),// Cached map { "articleId|fieldName": "value" }
    lastSyncedSnapshot: ''           // JSON string of last successful sync
};

/* ---------------------------------------------------------
   Helper: load / save field values (localStorage)
--------------------------------------------------------- */
function loadFieldValues() {
    const raw = localStorage.getItem(CONFIG.fieldValuesKey);
    return raw ? JSON.parse(raw) : {};
}
function saveFieldValues(map) {
    localStorage.setItem(CONFIG.fieldValuesKey, JSON.stringify(map));
}

/* Debounced write‑back (300 ms) */
const debouncedSaveFieldValues = debounce(map => {
    saveFieldValues(map);
}, 300);

/* Persist a single field value (called on every input event) */
function persistFieldValue(articleId, fieldName, value) {
    const key = `${articleId}|${fieldName}`;
    uiState.fieldValueMap[key] = value;
    debouncedSaveFieldValues(uiState.fieldValueMap);
}

/* ---------------------------------------------------------
   Library persistence (templates) – localStorage
--------------------------------------------------------- */
function loadLibrary() {
    const raw = localStorage.getItem(CONFIG.libraryKey);
    return raw ? JSON.parse(raw) : [];
}
function saveLibrary(lib) {
    localStorage.setItem(CONFIG.libraryKey, JSON.stringify(lib));
}

/* ---------------------------------------------------------
   Render the list of saved templates (library UI)
--------------------------------------------------------- */
function renderLibraryList() {
    const ul = document.getElementById('templateList');
    ul.innerHTML = '';
    uiState.library.forEach(tpl => {
        const li = document.createElement('li');
        li.textContent = tpl.name;

        const btn = document.createElement('button');
        btn.textContent = 'Use';
        btn.addEventListener('click', () => addArticleFromTemplate(tpl));
        li.appendChild(btn);
        ul.appendChild(li);
    });
}

/* ---------------------------------------------------------
   Add a new input row to the library form
--------------------------------------------------------- */
function addInputRow(container, type = 'text', label = '', name = '') {
    const row = document.createElement('div');
    row.className = 'input-row';

    const lbl = document.createElement('input');
    lbl.type = 'text';
    lbl.placeholder = 'Label';
    lbl.value = label;
    row.appendChild(lbl);

    const sel = document.createElement('select');
    ['text','email','number','date','checkbox','radio'].forEach(opt => {
        const o = document.createElement('option');
        o.value = opt;
        o.textContent = opt;
        if (opt === type) o.selected = true;
        sel.appendChild(o);
    });
    row.appendChild(sel);

    const nam = document.createElement('input');
    nam.type = 'text';
    nam.placeholder = 'Name';
    nam.value = name;
    row.appendChild(nam);

    const del = document.createElement('button');
    del.textContent = '✕';
    del.addEventListener('click', () => row.remove());
    row.appendChild(del);

    container.appendChild(row);
}

/* ---------------------------------------------------------
   Initialise the Library UI (called once on page load)
--------------------------------------------------------- */
function initLibraryUI() {
    // Load persisted templates
    uiState.library = loadLibrary();
    renderLibraryList();

    // ----- Library form submit (save new template) -----
    const libForm = document.getElementById('libraryForm');
    libForm.addEventListener('submit', e => {
        e.preventDefault();
        const name = document.getElementById('libName').value.trim();
        if (!name) return toast('Template name required', true);

        const rows = document.querySelectorAll('#inputsFieldset .input-row');
        const inputs = Array.from(rows).map(r => ({
            label: r.children[0].value.trim(),
            type:  r.children[1].value,
            name:  r.children[2].value.trim()
        }));

        uiState.library.push({ name, inputs });
        saveLibrary(uiState.library);
        renderLibraryList();
        libForm.reset();
        document.getElementById('inputsFieldset').innerHTML = '';
        toast('Template saved');
    });

    // ----- Add‑input button (adds a blank row) -----
    document.getElementById('addInputBtn')
            .addEventListener('click', () => addInputRow(document.getElementById('inputsFieldset')));
}

/* ---------------------------------------------------------
   Create an <article> (form instance) – includes ARIA labels
--------------------------------------------------------- */
function createArticleNode(tpl, parentArticle = null) {
    const article = document.createElement('article');
    const id = ++uiState.articleSeq;
    article.dataset.id = id;

    // ---- Header (title) ----
    const hdr = document.createElement('header');
    hdr.textContent = tpl.name;
    article.appendChild(hdr);

    // ---- Delete button ----
    const delBtn = document.createElement('button');
    delBtn.className = 'control';
    delBtn.setAttribute('aria-label', 'Delete article');
    delBtn.textContent = '✕';
    delBtn.addEventListener('click', () => {
        article.remove();
        generateAdsFromMain();
        scheduleSync(); // removal counts as a change
    });
    article.appendChild(delBtn);

    // ---- Toggle button ----
    const togBtn = document.createElement('button');
    togBtn.className = 'control';
    togBtn.style.right = '2.5rem';
    togBtn.setAttribute('aria-label', 'Toggle article visibility');
    togBtn.textContent = '▾';
    togBtn.addEventListener('click', () => {
        const body = article.querySelector('.body');
        if (body) {
            const hidden = body.hidden = !body.hidden;
            togBtn.textContent = hidden ? '▸' : '▾';
        }
    });
    article.appendChild(togBtn);

    // ---- Add child button ----
    const childBtn = document.createElement('button');
    childBtn.className = 'control';
    childBtn.style.right = '4.5rem';
    childBtn.setAttribute('aria-label', 'Add child article');
    childBtn.textContent = '+';
    childBtn.addEventListener('click', () => {
        const childName = prompt('Enter name of saved template to add as child:');
        if (!childName) return;
        const childTpl = uiState.library.find(t => t.name === childName);
        if (!childTpl) return toast('Template not found', true);
        addArticleFromTemplate(childTpl, article);
    });
    article.appendChild(childBtn);

    // ---- Body – actual form fields ----
    const bodyDiv = document.createElement('div');
    bodyDiv.className = 'body';
    tpl.inputs.forEach(inp => {
        const wrapper = document.createElement('div');
        const label = document.createElement('label');
        label.textContent = inp.label;

        const field = document.createElement('input');
        field.type = inp.type;
        field.name = inp.name;

        // Restore persisted value if any
        const persistedKey = `${id}|${inp.name}`;
        if (uiState.fieldValueMap[persistedKey] !== undefined) {
            field.value = uiState.fieldValueMap[persistedKey];
        }

        // Persist on every change (debounced)
        field.addEventListener('input', () => {
            persistFieldValue(id, inp.name, field.value);
            scheduleSync(); // any edit triggers auto‑sync debounce
        });

        label.appendChild(field);
        wrapper.appendChild(label);
        bodyDiv.appendChild(wrapper);
    });
    article.appendChild(bodyDiv);

    // ---- Insert into DOM (top‑level or as child) ----
    if (parentArticle) {
        const parentBody = parentArticle.querySelector('.body');
        parentBody.appendChild(article);
    } else {
        document.getElementById('mainContent').appendChild(article);
    }

    // Remove placeholder paragraph if present
    const placeholder = document.querySelector('#mainContent .placeholder');
    if (placeholder) placeholder.remove();

    generateAdsFromMain();
}

/* Public helper used by the library UI */
function addArticleFromTemplate(tpl, parentArticle = null) {
    createArticleNode(tpl, parentArticle);
}

/* ---------------------------------------------------------
   Keyword‑based ad generation (simple keyword mapping)
--------------------------------------------------------- */
function generateAdsFromMain() {
    const text = document.getElementById('mainContent').innerText.toLowerCase();
    const container = document.getElementById('adsContainer');
    container.innerHTML = '';

    const map = [
        { word: 'email',    ad: 'Secure your inbox with Proton Mail.' },
        { word: 'vpn',      ad: 'Browse safely using Proton VPN.' },
        { word: 'cloud',    ad: 'Store files privately with Proton Drive.' },
        { word: 'calendar', ad: 'Organise meetings with Proton Calendar.' }
    ];

    let any = false;
    map.forEach(m => {
        if (text.includes(m.word)) {
            any = true;
            const div = document.createElement('div');
            div.className = 'ad-item';
            div.textContent = m.ad;
            container.appendChild(div);
        }
    });

    if (!any) {
        const fallback = document.createElement('div');
        fallback.className = 'ad-item';
        fallback.textContent = 'Explore Proton’s privacy‑focused services.';
        container.appendChild(fallback);
    }
}

/* ---------------------------------------------------------
   Context export / import (JSON)
--------------------------------------------------------- */
function collectContext() {
    const articles = [];

    function walk(el, parentId = null) {
        const id = el.dataset.id;
        const title = el.querySelector('header').textContent;
        const inputs = Array.from(el.querySelectorAll('.body input')).map(inp => ({
            name:  inp.name,
            type:  inp.type,
            value: inp.value
        }));
        articles.push({ id, title, inputs, parentId });

        const childArts = el.querySelectorAll(':scope > .body > article');
        childArts.forEach(c => walk(c, id));
    }

    document.querySelectorAll('#mainContent > article').forEach(a => walk(a));
    return { version: 1, timestamp: Date.now(), articles };
}

function exportContext() {
    const payload = collectContext();
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `atp-context-${Date.now()}.json`;
    a.click();
    URL.revokeObjectURL(url);
}

/**
 * Import a JSON context file and rebuild the workspace.
 * Restores any persisted field values (overriding file values).
 */
async function importContext(file) {
    showSpinner();
    try{
        const reader = new FileReader();
        reader.onload = ev => {
            try {
                const data = JSON.parse(ev.target.result);
                if (!Array.isArray(data.articles)) throw new Error('Invalid format');

                // Clear current workspace
                document.getElementById('mainContent').innerHTML = '';

                // Map of id → article element (for nesting)
                const lookup = {};

                data.articles.forEach(rec => {
                    const article = document.createElement('article');
                    article.dataset.id = rec.id;

                    const hdr = document.createElement('header');
                    hdr.textContent = rec.title;
                    article.appendChild(hdr);

                    // Controls (same as createArticleNode)
                    const delBtn = document.createElement('button');
                    delBtn.className = 'control';
                    delBtn.setAttribute('aria-label', 'Delete article');
                    delBtn.textContent = '✕';
                    delBtn.addEventListener('click', () => {
                        article.remove();
                        generateAdsFromMain();
                        scheduleSync();
                    });
                    article.appendChild(delBtn);

                    const togBtn = document.createElement('button');
                    togBtn.className = 'control';
                    togBtn.style.right = '2.5rem';
                    togBtn.setAttribute('aria-label', 'Toggle article visibility');
                    togBtn.textContent = '▾';
                    togBtn.addEventListener('click', () => {
                        const body = article.querySelector('.body');
                        if (body) {
                            const hidden = body.hidden = !body.hidden;
                            togBtn.textContent = hidden ? '▸' : '▾';
                        }
                    });
                    article.appendChild(togBtn);

                    const childBtn = document.createElement('button');
                    childBtn.className = 'control';
                    childBtn.style.right = '4.5rem';
                    childBtn.setAttribute('aria-label', 'Add child article');
                    childBtn.textContent = '+';
                    childBtn.addEventListener('click', () => {
                        const childName = prompt('Enter name of saved template to add as child:');
                        if (!childName) return;
                        const childTpl = uiState.library.find(t => t.name === childName);
                        if (!childTpl) return toast('Template not found', true);
                        addArticleFromTemplate(childTpl, article);
                    });
                    article.appendChild(childBtn);

                    const bodyDiv = document.createElement('div');
                    bodyDiv.className = 'body';
                    rec.inputs.forEach(inp => {
                        const wrapper = document.createElement('div');
                        const label = document.createElement('label');
                        label.textContent = inp.name;
                        const field = document.createElement('input');
                        field.type = inp.type;
                        field.name = inp.name;
                        field.value = inp.value || '';

                        // Restore persisted value if any (overrides file value)
                        const persistedKey = `${rec.id}|${inp.name}`;
                        if (uiState.fieldValueMap[persistedKey] !== undefined) {
                            field.value = uiState.fieldValueMap[persistedKey];
                        }

                        // Persist on change (debounced)
                        field.addEventListener('input', () => {
                            persistFieldValue(rec.id, inp.name, field.value);
                            scheduleSync();
                        });

                        label.appendChild(field);
                        wrapper.appendChild(label);
                        bodyDiv.appendChild(wrapper);
                    });
                    article.appendChild(bodyDiv);

                    // Insert according to parentId
                    if (rec.parentId && lookup[rec.parentId]) {
                        const parentBody = lookup[rec.parentId].querySelector('.body');
                        parentBody.appendChild(article);
                    } else {
                        document.getElementById('mainContent').appendChild(article);
                    }
                    lookup[rec.id] = article;
                });

                generateAdsFromMain();
                toast('Context imported');
            } catch (e) {
                toast('Import failed: ' + e.message, true);
            }
        };
        reader.readAsText(file);
    } finally {
        hideSpinner();
    }
}

/* ---------------------------------------------------------
   Decrypt an encrypted context Blob (produced by upload/download)
--------------------------------------------------------- */
async function decryptContext(file) {
    showSpinner();
    try{
        if (!authState.cryptoKey) {
            toast('Set a password first (log in)', true);
            return;
        }
        const reader = new FileReader();
        reader.onload = async ev => {
            try {
                const b64 = ev.target.result.trim();
                const decrypted = await decryptData(b64, authState.cryptoKey);
                const data = JSON.parse(decrypted);

                // Replace workspace with decrypted data
                document.getElementById('mainContent').innerHTML = '';
                const tempBlob = new Blob([JSON.stringify(data)], { type: 'application/json' });
                await importContext(tempBlob);
                toast('Context decrypted & loaded');
            } catch (e) {
                toast('Decryption failed: ' + e.message, true);
            }
        };
        reader.readAsText(file);
    } finally {
        hideSpinner();
    }
}

/* ---------------------------------------------------------
   Search / filter (fluid & responsive)
--------------------------------------------------------- */
function articleMatchesSearch(articleEl, term) {
    const lowered = term.toLowerCase();
    const inputs = articleEl.querySelectorAll('input');
    for (const inp of inputs) {
        const label = inp.closest('label');
        const labelText = label ? label.textContent.trim().toLowerCase() : '';
        const placeholder = (inp.placeholder || '').toLowerCase();
        const name = (inp.name || '').toLowerCase();

        if (labelText.includes(lowered) ||
            placeholder.includes(lowered) ||
            name.includes(lowered)) {
            return true;
        }
    }
    return false;
}

/**
 * Hide/show articles based on the current search term.
 * Ancestors of matching articles stay visible.
 */
function filterArticles(term) {
    const root = document.getElementById('mainContent');
    const allArticles = root.querySelectorAll('article');

    // First pass – direct matches
    const matches = new Map(); // articleEl → boolean
    allArticles.forEach(a => matches.set(a, articleMatchesSearch(a, term)));

    // Second pass – propagate matches upward so parents stay visible
    function propagate(el) {
        if (!el) return false;
        const direct = matches.get(el);
        const childMatch = Array.from(el.children).some(ch => {
            if (ch.tagName.toLowerCase() === 'article') return propagate(ch);
            return false;
        });
        const keep = direct || childMatch;
        el.style.display = keep ? '' : 'none';
        return keep;
    }

    // Start from top‑level articles
    root.querySelectorAll(':scope > article').forEach(propagate);
}

/* ---------------------------------------------------------
   Initialise the whole workspace (bind top‑level UI)
--------------------------------------------------------- */
function initWorkspace() {
    /* ---------- 1️⃣ Initialise Library UI ---------- */
    initLibraryUI();

    /* ---------- 2️⃣ Authentication buttons ---------- */
    document.getElementById('loginBtn').addEventListener('click', login);
    document.getElementById('logoutBtn').addEventListener('click', logout);

    /* ---------- 3️⃣ Export / Import ---------- */
    document.getElementById('exportBtn')
            .addEventListener('click', exportContext);

    // Import file selector (JSON context)
    document.getElementById('importFile')
            .addEventListener('change', e => {
                const file = e.target.files[0];
                if (file) importContext(file);
                // Reset the input so the same file can be selected again later
                e.target.value = '';
            });

    /* ---------- 4️⃣ Encryption / Decryption ---------- */
    document.getElementById('encryptBtn')
            .addEventListener('click', async () => {
                if (!authState.cryptoKey) {
                    toast('Set a password first (log in)', true);
                    return;
                }
                const payload = JSON.stringify(collectContext());
                try {
                    const b64 = await encryptData(payload, authState.cryptoKey);
                    const blob = new Blob([b64], { type: 'text/plain' });
                    const url = URL.createObjectURL(blob);
                    const a = document.createElement('a');
                    a.href = url;
                    a.download = `atp-encrypted-${Date.now()}.txt`;
                    a.click();
                    URL.revokeObjectURL(url);
                    toast('Context encrypted & downloaded');
                } catch (e) {
                    toast('Encryption failed: ' + e.message, true);
                }
            });

    document.getElementById('decryptBtn')
            .addEventListener('click', async () => {
                if (!authState.cryptoKey) {
                    toast('Set a password first (log in)', true);
                    return;
                }
                const file = await new Promise(resolve => {
                    const input = document.createElement('input');
                    input.type = 'file';
                    input.accept = '.txt,.enc';
                    input.onchange = ev => resolve(ev.target.files[0]);
                    input.click();
                });
                if (file) await decryptContext(file);
            });

    /* ---------- 5️⃣ Sync (upload / download) ---------- */
    document.getElementById('syncUploadBtn')
            .addEventListener('click', async () => {
                const file = await new Promise(resolve => {
                    const input = document.createElement('input');
                    input.type = 'file';
                    input.accept = '.txt,.enc';
                    input.onchange = ev => resolve(ev.target.files[0]);
                    input.click();
                });
                if (file) await uploadEncryptedContext(file);
            });

    document.getElementById('syncDownloadBtn')
            .addEventListener('click', downloadEncryptedContext);

    /* ---------- 6️⃣ Manual “Push to server” button ---------- */
    document.getElementById('pushBtn')
            .addEventListener('click', pushCurrentState);

    /* ---------- 7️⃣ Search / filter (fluid & responsive) ---------- */
    const searchBox = document.getElementById('searchInput');
    if (searchBox) {
        // Debounce is already handled inside filterArticles via the
        // `searchDebounce` constant; we just call it on each input.
        searchBox.addEventListener('input', () => {
            const term = searchBox.value.trim();
            filterArticles(term);
        });
    }

    /* ---------- 8️⃣ Initial UI state (login/logout visibility) ---------- */
    if (authState.token) {
        document.getElementById('loginBtn').hidden = true;
        document.getElementById('logoutBtn').hidden = false;
    } else {
        document.getElementById('loginBtn').hidden = false;
        document.getElementById('logoutBtn').hidden = true;
    }

    /* ---------- 9️⃣ Generate ads for any pre‑existing content ---------- */
    generateAdsFromMain();
    //hideSpinner
    hideSpinner();
}

/* ---------------------------------------------------------
   Export public API (used by other modules)
--------------------------------------------------------- */
export {
    // State & persistence
    uiState,
    persistFieldValue,

    // Library UI
    renderLibraryList,
    addInputRow,

    // Article handling
    createArticleNode,
    addArticleFromTemplate,

    // Ads
    generateAdsFromMain,

    // Context export / import
    collectContext,
    exportContext,
    importContext,
    decryptContext,

    // Search / filter
    filterArticles,

    // Workspace initialiser
    initWorkspace,
};