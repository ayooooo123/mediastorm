(function () {
    'use strict';

    const desktopMedia = window.matchMedia('(min-width: 1100px)');
    const state = {
        grid: null,
        gridElement: null,
        layoutURL: '',
        canEdit: false,
        ready: false,
        fallback: false,
        editing: false,
        detailLevel: 'basic',
        detailBeforeEdit: 'basic',
        definitions: new Map(),
        moduleElements: new Map(),
        canonicalModules: [],
        editSnapshot: [],
        availability: new Map([['vod-stream-limits', false]]),
    };

    function cloneModules(modules) {
        return modules.map(module => ({
            id: String(module.id),
            x: Number(module.x),
            y: Number(module.y),
            w: Number(module.w),
            h: Number(module.h),
        }));
    }

    function moduleOptions(module) {
        const definition = state.definitions.get(module.id);
        return {
            id: module.id,
            x: module.x,
            y: module.y,
            w: module.w,
            h: module.h,
            minW: definition.minW,
            minH: definition.minH,
            maxW: definition.maxW,
            maxH: definition.maxH,
        };
    }

    function showMessage(message, kind) {
        const element = document.getElementById('dashboardLayoutMessage');
        if (!element) return;
        element.textContent = message || '';
        element.classList.toggle('success', kind === 'success');
        element.hidden = !message;
    }

    function setBusy(busy) {
        const saveButton = document.getElementById('dashboardSaveLayoutBtn');
        const actions = document.getElementById('dashboardLayoutActions');
        if (saveButton) {
            saveButton.disabled = busy;
            saveButton.textContent = busy ? 'Saving…' : 'Save';
        }
        actions?.querySelectorAll('button').forEach(button => {
            if (button !== saveButton) button.disabled = busy;
        });
    }

    function createModuleItem(source, module) {
        const item = document.createElement('div');
        item.className = 'grid-stack-item';
        item.dataset.moduleId = module.id;
        item.setAttribute('gs-id', module.id);

        const content = document.createElement('div');
        content.className = 'grid-stack-item-content';

        const handle = document.createElement('button');
        handle.type = 'button';
        handle.className = 'dashboard-module-handle';
        handle.setAttribute('aria-label', `Move or resize ${source.dataset.dashboardModuleLabel || module.id}`);
        handle.setAttribute('title', 'Drag to move. Arrow keys move; Shift + arrow keys resize.');
        handle.addEventListener('keydown', event => handleKeyboardLayout(event, item));

        source.classList.add('dashboard-module');
        source.style.removeProperty('display');
        content.append(source, handle);
        item.append(content);
        state.gridElement.append(item);
        state.moduleElements.set(module.id, item);
        return item;
    }

    function initializeGrid(payload) {
        if (!window.GridStack) throw new Error('The dashboard layout engine did not load.');
        if (!payload || payload.version !== 1 || payload.columns !== 12) {
            throw new Error('The server returned an unsupported dashboard layout.');
        }
        if (!Array.isArray(payload.modules) || !Array.isArray(payload.definitions)) {
            throw new Error('The server returned an incomplete dashboard layout.');
        }

        state.definitions = new Map(payload.definitions.map(definition => [definition.id, definition]));
        state.canonicalModules = cloneModules(payload.modules);
        const sources = new Map(
            Array.from(document.querySelectorAll('[data-dashboard-module]'))
                .map(source => [source.dataset.dashboardModule, source])
        );

        state.gridElement.replaceChildren();
        for (const module of state.canonicalModules) {
            const source = sources.get(module.id);
            const definition = state.definitions.get(module.id);
            if (!source || !definition) continue;
            if (!state.availability.has(module.id)) {
                state.availability.set(module.id, module.id !== 'vod-stream-limits');
            }
            createModuleItem(source, module);
        }
        document.querySelectorAll('[data-dashboard-module-source]').forEach(source => {
            source.hidden = true;
        });

        state.grid = window.GridStack.init({
            column: 12,
            cellHeight: 64,
            margin: 10,
            float: false,
            animate: true,
            disableDrag: true,
            disableResize: true,
            draggable: { handle: '.dashboard-module-handle', scroll: true },
            resizable: { handles: 'e,se,s,sw,w' },
            columnOpts: {
                breakpointForWindow: true,
                breakpoints: [{ w: 1099, c: 1, layout: 'list' }],
                layout: 'list',
            },
        }, state.gridElement);

        for (const module of state.canonicalModules) {
            const element = state.moduleElements.get(module.id);
            if (element?.gridstackNode) state.grid.update(element, moduleOptions(module));
        }
        state.grid.compact('list');
        state.ready = true;
        applyVisibility();
    }

    function currentCanonicalModule(id) {
        return state.canonicalModules.find(module => module.id === id);
    }

    function ensureModulePresent(id) {
        const element = state.moduleElements.get(id);
        const module = currentCanonicalModule(id);
        if (!element || !module) return;
        element.hidden = false;
        if (!element.gridstackNode) state.grid.makeWidget(element, moduleOptions(module));
    }

    function removeModuleFromGrid(id) {
        const element = state.moduleElements.get(id);
        if (!element) return;
        if (element.gridstackNode) state.grid.removeWidget(element, false, false);
        element.hidden = true;
    }

    function visibleModuleIDs(forceAll) {
        const ids = [];
        for (const module of state.canonicalModules) {
            if (!state.moduleElements.has(module.id)) continue;
            const definition = state.definitions.get(module.id);
            const available = state.availability.get(module.id) !== false;
            if (forceAll || ((!definition.advanced || state.detailLevel === 'advanced') && available)) {
                ids.push(module.id);
            }
        }
        return ids;
    }

    function applyVisibility(forceAll) {
        if (!state.ready || !state.grid) return;
        const visible = new Set(visibleModuleIDs(Boolean(forceAll)));
        state.grid.batchUpdate();

        for (const module of state.canonicalModules) {
            if (visible.has(module.id)) ensureModulePresent(module.id);
        }
        const visibleLayout = state.canonicalModules
            .filter(module => visible.has(module.id))
            .map(moduleOptions);
        state.grid.load(visibleLayout, false);

        for (const module of state.canonicalModules) {
            if (!visible.has(module.id)) removeModuleFromGrid(module.id);
        }
        state.grid.compact('list');
        state.grid.batchUpdate(false);
    }

    function serializeGrid() {
        return state.canonicalModules.map(existing => {
            const element = state.moduleElements.get(existing.id);
            if (!element?.gridstackNode) return { ...existing };
            return {
                id: existing.id,
                x: Number(element.getAttribute('gs-x') || 0),
                y: Number(element.getAttribute('gs-y') || 0),
                w: Number(element.getAttribute('gs-w') || 1),
                h: Number(element.getAttribute('gs-h') || 1),
            };
        });
    }

    function setEditingControls(editing) {
        const editButton = document.getElementById('dashboardEditLayoutBtn');
        const actions = document.getElementById('dashboardLayoutActions');
        if (editButton) editButton.hidden = editing;
        if (actions) actions.hidden = !editing;
        document.getElementById('dashboardBasicBtn')?.toggleAttribute('disabled', editing);
        document.getElementById('dashboardAdvancedBtn')?.toggleAttribute('disabled', editing);
        state.gridElement.classList.toggle('dashboard-layout-editing', editing);
    }

    function finishEditing() {
        state.editing = false;
        state.grid.enableMove(false);
        state.grid.enableResize(false);
        setEditingControls(false);
        state.detailLevel = state.detailBeforeEdit;
        applyVisibility(false);
    }

    function startEditing() {
        if (!state.ready || !state.canEdit) return;
        if (!desktopMedia.matches) {
            showMessage('Dashboard layout editing is available on desktop screens only.', 'error');
            return;
        }
        if (state.editing) return;

        showMessage('', 'error');
        state.editing = true;
        state.detailBeforeEdit = state.detailLevel;
        state.editSnapshot = cloneModules(state.canonicalModules);
        state.detailLevel = 'advanced';
        applyVisibility(true);
        state.grid.enableMove(true);
        state.grid.enableResize(true);
        setEditingControls(true);
        state.moduleElements.values().next().value?.querySelector('.dashboard-module-handle')?.focus();
    }

    function cancelEditing(options) {
        if (!state.editing) return;
        state.canonicalModules = cloneModules(state.editSnapshot);
        state.grid.load(state.canonicalModules.map(moduleOptions), false);
        finishEditing();
        if (!options?.silent) showMessage('Layout changes were discarded.', 'success');
    }

    function resetDraft() {
        if (!state.editing) return;
        const defaults = state.canonicalModules.map(module => {
            const definition = state.definitions.get(module.id);
            return {
                id: module.id,
                x: definition.defaultX,
                y: definition.defaultY,
                w: definition.defaultW,
                h: definition.defaultH,
            };
        });
        state.grid.load(defaults.map(moduleOptions), false);
        state.grid.compact('list');
        showMessage('Default arrangement loaded. Select Save to make it global.', 'success');
    }

    async function save() {
        if (!state.editing) return;
        setBusy(true);
        showMessage('', 'error');
        try {
            const response = await fetch(state.layoutURL, {
                method: 'PUT',
                credentials: 'same-origin',
                headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
                body: JSON.stringify({ version: 1, modules: serializeGrid() }),
            });
            if (!response.ok) {
                const message = (await response.text()).trim();
                throw new Error(message || `Save failed with status ${response.status}`);
            }
            const payload = await response.json();
            state.canonicalModules = cloneModules(payload.modules);
            state.editSnapshot = cloneModules(payload.modules);
            finishEditing();
            showMessage('Dashboard layout saved for all administrators.', 'success');
        } catch (error) {
            showMessage(`Unable to save the dashboard layout. ${error.message}`, 'error');
        } finally {
            setBusy(false);
        }
    }

    function handleKeyboardLayout(event, item) {
        if (!state.editing || !item.gridstackNode) return;
        const directions = {
            ArrowLeft: [-1, 0],
            ArrowRight: [1, 0],
            ArrowUp: [0, -1],
            ArrowDown: [0, 1],
        };
        const direction = directions[event.key];
        if (!direction) return;
        event.preventDefault();

        const node = item.gridstackNode;
        const definition = state.definitions.get(item.dataset.moduleId);
        if (event.shiftKey) {
            const width = Math.max(definition.minW, Math.min(definition.maxW, node.w + direction[0]));
            const height = Math.max(definition.minH, Math.min(definition.maxH, node.h + direction[1]));
            state.grid.update(item, { w: Math.min(width, 12 - node.x), h: height });
        } else {
            state.grid.update(item, {
                x: Math.max(0, Math.min(12 - node.w, node.x + direction[0])),
                y: Math.max(0, node.y + direction[1]),
            });
        }
    }

    function setDetailLevel(level) {
        state.detailLevel = level === 'advanced' ? 'advanced' : 'basic';
        if (state.fallback) {
            applyFallbackVisibility();
            return;
        }
        if (!state.editing) applyVisibility(false);
    }

    function setModuleAvailable(id, available) {
        state.availability.set(id, Boolean(available));
        if (state.fallback) {
            applyFallbackVisibility();
            return;
        }
        if (!state.editing) applyVisibility(false);
    }

    function applyFallbackVisibility() {
        document.querySelectorAll('.dashboard-module').forEach(element => {
            const id = element.dataset.dashboardModule;
            const unavailable = state.availability.get(id) === false;
            const hiddenByDetail = element.classList.contains('dashboard-advanced-detail') && state.detailLevel !== 'advanced';
            element.style.display = unavailable || hiddenByDetail ? 'none' : '';
        });
    }

    function initializeFallback(error) {
        console.error('Dashboard layout initialization failed', error);
        state.fallback = true;
        state.gridElement.replaceChildren();
        state.gridElement.classList.add('dashboard-grid-fallback');
        document.querySelectorAll('[data-dashboard-module]').forEach(source => {
            source.classList.add('dashboard-module');
            state.gridElement.append(source);
        });
        document.querySelectorAll('[data-dashboard-module-source]').forEach(source => {
            source.hidden = true;
        });
        applyFallbackVisibility();
        showMessage('The saved dashboard layout could not be loaded. Showing the safe default flow.', 'error');
    }

    async function initialize() {
        state.gridElement = document.getElementById('dashboardGrid');
        if (!state.gridElement) return;
        state.layoutURL = state.gridElement.dataset.layoutUrl;
        state.canEdit = state.gridElement.dataset.canEdit === 'true';
        state.detailLevel = localStorage.getItem('mediastorm-dashboard-detail') === 'advanced' ? 'advanced' : 'basic';
        try {
            const response = await fetch(state.layoutURL, {
                credentials: 'same-origin',
                headers: { Accept: 'application/json' },
            });
            if (!response.ok) throw new Error(`Layout request failed with status ${response.status}`);
            initializeGrid(await response.json());
        } catch (error) {
            initializeFallback(error);
        }
    }

    desktopMedia.addEventListener('change', event => {
        if (!event.matches && state.editing) {
            cancelEditing({ silent: true });
            showMessage('Layout changes were discarded because the window left desktop size.', 'error');
        }
    });

    window.addEventListener('beforeunload', event => {
        if (!state.editing) return;
        event.preventDefault();
        event.returnValue = '';
    });

    window.dashboardLayoutController = {
        isReady: () => state.ready,
        startEditing,
        cancelEditing,
        resetDraft,
        save,
        setDetailLevel,
        setModuleAvailable,
    };

    document.addEventListener('DOMContentLoaded', initialize);
})();
