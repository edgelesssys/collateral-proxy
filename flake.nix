# Copyright 2026 Edgeless Systems GmbH
# SPDX-License-Identifier: BUSL-1.1
{
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs?ref=nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    treefmt-nix = {
      url = "github:numtide/treefmt-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      treefmt-nix,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ (import ./overlays/nixpkgs.nix) ];
          config.allowUnfreePredicate = pkg: (pkg.pname or "") == "collateral-proxy";
        };
        inherit (pkgs) lib;
        version = lib.trim (builtins.readFile ./version.txt);
        image = "ghcr.io/edgelesssys/collateral-proxy";
        treefmtEval = treefmt-nix.lib.evalModule pkgs ./treefmt.nix;

        collateral-proxy = pkgs.buildGoModule {
          pname = "collateral-proxy";
          inherit version;
          src = lib.fileset.toSource {
            root = ./.;
            fileset = lib.fileset.unions [
              ./go.mod
              ./go.sum
              (lib.fileset.fileFilter (file: lib.hasSuffix ".go" file.name) ./.)
            ];
          };
          proxyVendor = true;
          vendorHash = "sha256-GrNc8vmx8p2Cb0FeGyeVKlxYve5KvhZpID/A9MLi/Sw=";
          subPackages = [ "." ];
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-X main.version=v${version}"
          ];
          # Race detector needs cgo.
          preCheck = "export CGO_ENABLED=1";
          checkPhase = ''
            runHook preCheck
            go test -race ./...
            runHook postCheck
          '';
          meta = {
            description = "Read-through caching forward proxy for attestation collateral (AMD KDS, Intel PCS, NVIDIA RIM).";
            license = lib.licenses.bsl11;
            mainProgram = "collateral-proxy";
          };
        };

        # OCI image published to ${image}.
        container = pkgs.dockerTools.buildImage {
          name = "collateral-proxy";
          tag = "v${version}";
          copyToRoot = with pkgs.dockerTools; [ caCertificates ];
          config.Entrypoint = [ (lib.getExe collateral-proxy) ];
        };

        # Push the container image  and echo the pinned reference ${image}:<tag>@sha256:<digest> to stdout.
        push = pkgs.writeShellApplication {
          name = "push-collateral-proxy";
          runtimeInputs = with pkgs; [
            crane
            gzip
          ];
          text = ''
            trap 'echo "push-collateral-proxy: failed (exit $?) at line $LINENO: $BASH_COMMAND" >&2' ERR
            tag="''${1:-dev}"
            tmp=$(mktemp)
            trap 'rm -f "$tmp"' EXIT
            echo "push-collateral-proxy: pushing ${image}:$tag" >&2
            gunzip < "${container}" > "$tmp"
            crane push "$tmp" "${image}:$tag" >&2
            digest=$(crane digest "${image}:$tag")
            echo "${image}:$tag@$digest"
          '';
        };

        # Render the deployment manifest to stdout.
        render-k8s-resources = pkgs.writeShellApplication {
          name = "render-k8s-resources";
          runtimeInputs = [
            pkgs.gnugrep
            pkgs.gnused
          ];
          text = ''
            trap 'echo "render-k8s-resources: failed (exit $?) at line $LINENO: $BASH_COMMAND" >&2' ERR
            if [[ $# -ne 1 ]]; then
              echo "usage: render-k8s-resources ${image}:<tag>@sha256:<digest>" >&2
              exit 1
            fi
            ref=$1
            template=${./collateral-proxy.yml}
            if ! grep -q '%%pin%%' "$template"; then
              echo "render-k8s-resources: template $template is missing the %%pin%% placeholder" >&2
              exit 1
            fi
            sed "s|%%pin%%|$ref|" "$template"
          '';
        };

        # Lint the working tree: `nix run .#lint`. Extra args pass through, e.g. `nix run .#lint -- --fix`.
        lint = pkgs.writeShellApplication {
          name = "lint";
          runtimeInputs = [
            pkgs.golangci-lint
            pkgs.go
          ];
          text = ''exec golangci-lint run "$@"'';
        };

        # Scan for known vulnerabilities: `nix run .#govulncheck`.
        govulncheck = pkgs.writeShellApplication {
          name = "govulncheck";
          runtimeInputs = [
            pkgs.govulncheck
            pkgs.go
          ];
          text = "exec govulncheck ./...";
        };
      in
      {
        packages = {
          default = collateral-proxy;
          inherit
            collateral-proxy
            container
            push
            render-k8s-resources
            lint
            govulncheck
            ;
        };

        formatter = treefmtEval.config.build.wrapper;

        # `nix flake check` runs formatters and tests.
        checks = {
          formatting = treefmtEval.config.build.check self;
          # Building the package runs `go test -race ./...` in its checkPhase.
          tests = collateral-proxy;
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            golangci-lint
            gotools
            gopls
            crane
            govulncheck
          ];
        };
      }
    );
}
