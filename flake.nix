{
  description = ''
    CLIProxyAPI (Waffleophagus fork) — Nix flake.

    Packages the fork's own source (`self`), so every commit is packaged as-is.
    Outputs:
      packages.<system>.cliproxyapi          — Go server binary (placeholder web UI)
      packages.<system>.cliproxyapi-with-ui  — server with the Svelte management UI embedded
      packages.<system>.default              — alias for cliproxyapi-with-ui
      overlays.default                       — `cliproxyapi` / `cliproxyapi-with-ui` into pkgs
      nixosModules.default                   — `services.cliproxyapi` (systemd service)
  '';

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      overlays.default =
        final: prev: {
          cliproxyapi = self.packages.${prev.system}.cliproxyapi;
          cliproxyapi-with-ui = self.packages.${prev.system}.cliproxyapi-with-ui;
        };

      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          version = self.rev or self.dirtyRev or "unknown";
          mkPackage =
            extraArgs:
            pkgs.callPackage ./nix/package.nix (
              {
                inherit version;
                src = self;
              }
              // extraArgs
            );
        in
        {
          cliproxyapi = mkPackage { };
          cliproxyapi-with-ui = mkPackage { withWebUI = true; };
          default = self.packages.${system}.cliproxyapi-with-ui;
        }
      );

      nixosModules.default = import ./nix/module.nix self;

      checks = forAllSystems (system: {
        cliproxyapi = self.packages.${system}.cliproxyapi;
        cliproxyapi-with-ui = self.packages.${system}.cliproxyapi-with-ui;
      });
    };
}
