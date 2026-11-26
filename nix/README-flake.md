# Nix flake

This repo is importable as a Nix flake. The flake packages **this fork's own
source**, so every commit is packaged as-is — no src-hash maintenance, ever.
The only hashes that can go stale are the Go vendor hash and the pnpm store
hash (they change only when `go.mod`/`go.sum` or `web/pnpm-lock.yaml` change),
and `.github/workflows/flake.yml` auto-fixes and commits those.

## Quick start (NixOS)

```nix
# flake.nix of your config
{
  inputs.cliproxyapi.url = "github:Waffleophagus/CLIProxyAPI";
  # optional: keep nixpkgs consistent
  # inputs.cliproxyapi.inputs.nixpkgs.follows = "nixpkgs";
}
```

```nix
# in your NixOS configuration
{ inputs, ... }: {
  imports = [ inputs.cliproxyapi.nixosModules.default ];

  services.cliproxyapi = {
    enable = true;
    openFirewall = false;          # keep it local unless you need otherwise
    settings = {
      server.host = "127.0.0.1";
      server.port = 8317;
      # secrets from files, substituted at service start:
      # access.api-keys = [ { _secret = "/run/secrets/cliproxyapi-api-key"; } ];
    };
  };
}
```

The module renders `settings` to `/var/lib/cliproxyapi/config.yaml` on every
start; any `._secret = "/path"` value is replaced with the contents of that
file (passed in via systemd `LoadCredential`), so keys never live in the Nix
store. OAuth logins persist in the `StateDirectory`
(`/var/lib/cliproxyapi`).

## Outputs

- `packages.<system>.cliproxyapi` — server binary, placeholder web UI
- `packages.<system>.cliproxyapi-with-ui` — server with the Svelte management
  UI built and embedded (default)
- `packages.<system>.default` — alias for `cliproxyapi-with-ui`
- `overlays.default` — adds `cliproxyapi` / `cliproxyapi-with-ui` to pkgs
- `nixosModules.default` — `services.cliproxyapi`

To run the UI-less build with the management UI served as the placeholder
page, set `services.cliproxyapi.package = inputs.cliproxyapi.packages.${pkgs.system}.cliproxyapi;`.

## Notes

- The default nixpkgs input is pinned to `nixos-26.05`; PR it if you need
  something else.
- `services.cliproxyapi` here is modeled on the upstream module merged in
  nixpkgs (nixos-unstable), so configs port over 1:1.
- Version string = the git commit the package was built from.
