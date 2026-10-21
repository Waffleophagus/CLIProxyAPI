export type ConfirmRequest = {
  title: string;
  message: string;
  confirmLabel?: string;
  danger?: boolean;
  resolve: (ok: boolean) => void;
};

class ConfirmState {
  current = $state<ConfirmRequest | null>(null);
}

export const confirmState = new ConfirmState();

/** Opens the shared confirmation dialog and resolves with the user's choice. */
export function confirm(opts: Omit<ConfirmRequest, 'resolve'>): Promise<boolean> {
  return new Promise((resolve) => {
    confirmState.current?.resolve(false);
    confirmState.current = { ...opts, resolve };
  });
}
