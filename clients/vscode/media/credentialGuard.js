// ONE GUARD FOR EVERY FORM THAT TAKES A KEY IN THE CHAT.
//
// While a form that can hold a key is on screen, the rest of the chat is inert:
// no click, no focus and no keystroke reaches the composer, the /mcp dialog,
// the pages or the header, and inputEl.focus() from anywhere (a turn ending,
// a dialog closing) does nothing. The keyboard stays in the form: stray focus
// is sent back, a mousedown outside it is cancelled, and Tab goes round the
// form's own controls.
//
// FOUND 2026-11-08 (UI audit), twice:
// - the /mcp "Set key…" form opened under the /mcp dialog; a click on the part
//   of the key field that showed closed the dialog and handed the caret to the
//   composer, and the key typed next went into the chat (PR #91);
// - the connect popup's key step (/connect, /account's "Switch provider or
//   key") sits right above the composer, which stayed clickable; a click there,
//   Tab from the key field, or an answer finishing put the caret in the
//   composer, and the key typed next went into the chat.
// Both forms use this file, so the two cannot drift apart.
//
// main.js keeps a second wall on the composer itself (credentialFormOpen):
// it refuses keys, input, pastes and drops while a key form is on screen.
//
// One form is guarded at a time; taking a second hands the guard over.

(function () {
  'use strict';

  /**
   * @typedef {{
   *   take: (field?: HTMLElement | null) => void,
   *   release: (restore?: boolean | (() => void)) => void,
   *   isOn: () => boolean,
   *   focusField: () => void,
   * }} Guard
   */

  /** The form guarded now, with its container. @type {{ guard: Guard, el: HTMLElement } | null} */
  let current = null;

  /**
   * @param {HTMLElement} container the form: everything outside it goes inert
   * @param {{ firstField: () => HTMLElement | null }} opts where the keyboard belongs
   * @returns {Guard}
   */
  function create(container, opts) {
    /** What isolate() made inert, so release() undoes only that. @type {Element[]} */
    let madeInert = [];
    /** Where the keyboard was before the form took it. @type {HTMLElement | null} */
    let returnFocus = null;
    let on = false;

    function isolate() {
      unInert();
      for (let node = container; node && node.parentElement && node !== document.body; node = node.parentElement) {
        for (const sib of Array.from(node.parentElement.children)) {
          if (sib === node || sib.hasAttribute('inert') || sib.tagName === 'SCRIPT') continue;
          sib.setAttribute('inert', '');
          madeInert.push(sib);
        }
      }
    }

    function unInert() {
      for (const e of madeInert) e.removeAttribute('inert');
      madeInert = [];
    }

    function focusField() {
      if (!on) return;
      if (document.activeElement && container.contains(document.activeElement)) return;
      const f = opts.firstField();
      if (f) f.focus();
    }

    /** @type {Guard} */
    const guard = {
      take(field) {
        if (current && current.guard !== guard) current.guard.release(false);
        if (!on) {
          const a = document.activeElement;
          returnFocus = a instanceof HTMLElement && a !== document.body && !container.contains(a) ? a : null;
        }
        on = true;
        current = { guard, el: container };
        isolate();
        if (field) field.focus();
        focusField();
      },
      /**
       * Undoes the guard. restore: true puts the keyboard back where it was
       * before (when that is still on screen); a function is called instead
       * when it is not; false leaves the keyboard where it is.
       */
      release(restore) {
        if (!on) return;
        on = false;
        unInert();
        if (current && current.guard === guard) current = null;
        const back = returnFocus;
        returnFocus = null;
        if (restore === false || restore === undefined) return;
        if (back && back.isConnected && back.getClientRects().length > 0) back.focus();
        else if (typeof restore === 'function') restore();
      },
      isOn: () => on,
      focusField,
    };

    // Tab goes round the form's own controls, as in any modal dialog, rather
    // than out of it to the composer.
    container.addEventListener('keydown', (e) => {
      if (!on || e.key !== 'Tab') return;
      const all = Array.from(container.querySelectorAll('button, input, select, textarea, [tabindex="0"]')).filter(
        (x) => !(/** @type {HTMLButtonElement} */ (x).disabled) && x.getClientRects().length > 0,
      );
      if (all.length === 0) return;
      const first = /** @type {HTMLElement} */ (all[0]);
      const last = /** @type {HTMLElement} */ (all[all.length - 1]);
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    });

    return guard;
  }

  // The keyboard stays in the guarded form. Inert already refuses focus
  // outside it; this catches whatever gets there anyway (a node added after
  // the guard was taken, the frame regaining focus) and sends it back.
  document.addEventListener(
    'focusin',
    (e) => {
      if (current && e.target instanceof Node && !current.el.contains(e.target)) current.guard.focusField();
    },
    true,
  );
  // A click outside the form lands on the page body (inert content is
  // skipped), whose default would move the keyboard there -- out of the
  // field, so the next keystroke would go nowhere. Kept in the field instead.
  document.addEventListener(
    'mousedown',
    (e) => {
      if (current && e.target instanceof Node && !current.el.contains(e.target)) {
        e.preventDefault();
        current.guard.focusField();
      }
    },
    true,
  );

  window.MochiiiCredentialGuard = {
    create,
    /** Whether a key form is guarded now. */
    isOn: () => current !== null,
    /** Sends the keyboard back to the guarded form, if there is one. */
    focusField: () => {
      if (current) current.guard.focusField();
    },
  };
})();
