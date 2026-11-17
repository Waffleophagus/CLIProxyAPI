# Builds the CLIProxyAPI server from the fork's own tree (`src = self` in flake.nix).
#
# Two flavors:
#   withWebUI = false — Go binary only; the embedded management UI serves a
#                       built-in placeholder page (the code falls back when
#                       internal/webui/dist has no index.html).
#   withWebUI = true  — builds the Svelte UI with pnpm (separate derivation,
#                       so the pnpm hooks never leak into buildGoModule's
#                       go-modules build) and embeds it.
#
# vendorHash / pnpmHash only change when go.mod|go.sum or web/pnpm-lock.yaml
# change. .github/workflows/flake.yml re-derives and commits them automatically.
{
  lib,
  buildGoModule,
  stdenv,
  nodejs,
  pnpm_10_latest,
  pnpmConfigHook,
  pnpmBuildHook,
  fetchPnpmDeps,
  src,
  version,
  withWebUI ? false,
}:

let
  # Go module dependency set hash (from `go mod download` vendoring).
  vendorHash = "sha256-y6cwoDAMih5dcLS7o+cO7KJSA8sg6dM3xiJEPMyfd9U=";

  # pnpm store hash for web/pnpm-lock.yaml (fetcherVersion 5; only used when withWebUI).
  pnpmHash = "sha256-KG8l/RyT4s5Omv+aQgtdQtHGm+3+PyvBlFgSUiRUvWc=";

  # The Svelte management UI (web/), built standalone. Vite's outDir is
  # ../internal/webui/dist relative to web/, i.e. /build/internal/webui/dist
  # inside the sandbox — installPhase collects it from there.
  webui = stdenv.mkDerivation (finalAttrs: {
    pname = "cliproxyapi-webui";
    inherit version;
    src = "${src}/web";

    nativeBuildInputs = [
      nodejs
      pnpm_10_latest
      pnpmConfigHook
      pnpmBuildHook
    ];

    pnpmDeps = fetchPnpmDeps {
      inherit (finalAttrs) pname version;
      pnpm = pnpm_10_latest;
      src = "${src}/web";
      fetcherVersion = 5;
      hash = pnpmHash;
    };

    # pnpmBuildHook's default `pnpm run build` is exactly what we want
    # (web/package.json build = clean-dist.mjs && vite build); only the
    # install phase needs overriding since vite writes outside web/.
    installPhase = ''
      runHook preInstall
      mkdir -p $out
      cp -r ../internal/webui/dist/. $out/
      runHook postInstall
    '';

    meta.license = lib.licenses.mit;
  });
in

buildGoModule (finalAttrs: {
  pname = if withWebUI then "cliproxyapi-with-ui" else "cliproxyapi";
  inherit version src vendorHash;

  # Matches the Dockerfile: static binary, no cgo.
  env.CGO_ENABLED = "0";

  subPackages = [ "cmd/server" ];

  ldflags = [
    "-s"
    "-w"
    "-X main.Version=${version}"
    "-X main.Commit=${version}"
    "-X main.BuildDate=1970-01-01"
  ];

  # Stage the prebuilt UI where the Go embed expects it (runs in both the
  # go-modules and main derivations — harmless there, load-bearing here).
  postPatch = lib.optionalString withWebUI ''
    mkdir -p internal/webui/dist
    cp -r ${webui}/. internal/webui/dist/
  '';

  postInstall = ''
    mv $out/bin/server $out/bin/cliproxyapi
  '';

  # Note: unlike upstream, the fork prints its version banner at startup and
  # does NOT support `--version`, so no versionCheckHook here.

  meta = {
    description = "Proxy that provides OpenAI/Gemini/Claude/Codex/Grok compatible API interfaces (Waffleophagus fork)";
    license = lib.licenses.mit;
    mainProgram = "cliproxyapi";
    platforms = lib.platforms.unix;
  };
})
