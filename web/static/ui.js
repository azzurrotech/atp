// ui.js – tiny entry point that starts the app
import { hideSpinner } from './utils.js';
import { initWorkspace } from './workspace.js';

document.addEventListener('DOMContentLoaded', () => {
    initWorkspace();
    hideSpinner();
});