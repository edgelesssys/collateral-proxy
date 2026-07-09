# Copyright 2026 Edgeless Systems GmbH
# SPDX-License-Identifier: BUSL-1.1

{ lib, pkgs, ... }:
{
  projectRootFile = "flake.nix";
  programs = {
    # keep-sorted start block=true
    actionlint.enable = true;
    deadnix.enable = true;
    gofumpt.enable = true;
    keep-sorted.enable = true;
    nixfmt.enable = true;
    shellcheck.enable = true;
    shfmt.enable = true;
    statix.enable = true;
    yamlfmt.enable = true;
    # keep-sorted end
  };
  settings.formatter = {
    addlicense = {
      command = "${lib.getExe pkgs.addlicense}";
      options = [
        "-c=Edgeless Systems GmbH"
        "-s=only"
        "-l=BUSL-1.1"
      ];
      includes = [
        "*.go"
        "*.nix"
        "*.sh"
      ];
    };
  };
}
