import { describe, expect, it } from 'vitest';
import { copyHost } from './clipboard';

type Fake = { name: string; modal?: boolean; parentDialog?: Fake | null; closest(sel: string): Fake | null; matches(sel: string): boolean };

function el(name: string, opts: { modal?: boolean; parentDialog?: Fake | null } = {}): Fake {
  const node: Fake = {
    name,
    modal: opts.modal,
    parentDialog: opts.parentDialog ?? null,
    closest: (sel) => (sel === 'dialog[open]' ? (node.parentDialog ?? null) : null),
    matches: (sel) => sel === ':modal' && node.modal === true
  };
  return node;
}

function doc(open: Fake[]) {
  const body = el('body');
  return { body, querySelectorAll: (sel: string) => (sel === 'dialog[open]' ? open : []) };
}

describe('clipboard fallback host', () => {
  it('uses the dialog that contains the copy button', () => {
    const outer = el('outer', { modal: true });
    const inner = el('inner', { modal: true });
    const button = el('button', { parentDialog: outer });
    expect(copyHost(button, doc([outer, inner])).name).toBe('outer');
  });

  it('falls back to the topmost open modal dialog', () => {
    const a = el('a', { modal: true });
    const b = el('b', { modal: true });
    const nonModal = el('drawer', { modal: false });
    expect(copyHost(null, doc([a, b, nonModal])).name).toBe('b');
  });

  it('uses body when no modal dialog is open', () => {
    expect(copyHost(undefined, doc([el('plain', { modal: false })])).name).toBe('body');
    expect(copyHost(el('button'), doc([])).name).toBe('body');
  });
});
