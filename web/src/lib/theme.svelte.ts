import { storageGet, storageRemove, storageSet } from './storage';

export type ThemeChoice = 'system' | 'light' | 'dark';

const KEY = 'cliproxy.theme';

class Theme {
  choice = $state<ThemeChoice>('system');

  init() {
    const saved = storageGet('local', KEY);
    if (saved === 'light' || saved === 'dark') this.choice = saved;
    this.apply();
  }
  set(choice: ThemeChoice) {
    this.choice = choice;
    if (choice === 'system') storageRemove('local', KEY);
    else storageSet('local', KEY, choice);
    this.apply();
  }
  cycle() {
    this.set(this.choice === 'system' ? 'light' : this.choice === 'light' ? 'dark' : 'system');
  }
  apply() {
    const root = document.documentElement;
    if (this.choice === 'system') root.removeAttribute('data-theme');
    else root.setAttribute('data-theme', this.choice);
  }
}

export const theme = new Theme();
