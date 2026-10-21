export type Toast = { id: number; kind: 'ok' | 'error' | 'info'; text: string };

let next = 1;

class Toasts {
  items = $state<Toast[]>([]);

  push(kind: Toast['kind'], text: string, ms = kind === 'error' ? 7000 : 3500) {
    const id = next++;
    this.items = [...this.items, { id, kind, text }];
    setTimeout(() => this.dismiss(id), ms);
  }
  ok(text: string) {
    this.push('ok', text);
  }
  error(text: string) {
    this.push('error', text);
  }
  info(text: string) {
    this.push('info', text);
  }
  dismiss(id: number) {
    this.items = this.items.filter((t) => t.id !== id);
  }
}

export const toasts = new Toasts();
