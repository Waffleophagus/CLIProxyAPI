// navigator.clipboard needs a secure context (HTTPS or localhost). Over plain
// LAN HTTP fall back to a hidden textarea + execCommand.

type ElementLike = { closest(selector: string): unknown; matches?(selector: string): boolean };
type DocumentLike<E> = { body: E; querySelectorAll(selector: string): ArrayLike<E> };

function isModal(el: ElementLike): boolean {
  try {
    return el.matches?.(':modal') ?? true;
  } catch {
    // :modal is unsupported; treat any open dialog as modal.
    return true;
  }
}

/**
 * Picks where the temporary textarea must live. While a dialog is open via
 * showModal() everything outside it is inert, so a textarea under <body>
 * cannot be focused or selected. Prefer the dialog that contains the element
 * that triggered the copy, then the topmost (last opened) modal dialog.
 */
export function copyHost<E extends ElementLike>(anchor: ElementLike | null | undefined, doc: DocumentLike<E>): E {
  const own = anchor?.closest('dialog[open]');
  if (own) return own as E;
  const open = Array.from(doc.querySelectorAll('dialog[open]'));
  for (let i = open.length - 1; i >= 0; i--) {
    if (isModal(open[i])) return open[i];
  }
  return doc.body;
}

function legacyCopy(text: string, anchor: Element | null | undefined): boolean {
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.setAttribute('aria-hidden', 'true');
  ta.tabIndex = -1;
  ta.style.position = 'fixed';
  ta.style.top = '0';
  ta.style.left = '0';
  ta.style.width = '1px';
  ta.style.height = '1px';
  ta.style.opacity = '0';
  copyHost(anchor, document).appendChild(ta);
  let ok: boolean;
  try {
    ta.focus({ preventScroll: true });
    ta.select();
    ta.setSelectionRange(0, text.length);
    ok = document.activeElement === ta && document.execCommand('copy');
  } catch {
    ok = false;
  } finally {
    ta.remove();
    previous?.focus({ preventScroll: true });
  }
  return ok;
}

/**
 * Copies text to the clipboard. Pass the triggering element so the fallback
 * works inside modal dialogs. Returns false when copying failed; callers
 * should then let the user select the text manually.
 */
export async function copyText(text: string, anchor?: Element | null): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Fall through to the legacy path.
    }
  }
  return legacyCopy(text, anchor);
}

/** Saves text as a file via a temporary object URL. */
export function downloadText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
