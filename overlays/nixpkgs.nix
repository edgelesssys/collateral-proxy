# Copyright 2026 Edgeless Systems GmbH
# SPDX-License-Identifier: BUSL-1.1

final: prev:

{
  go_1_26 = prev.go_1_26.overrideAttrs (
    finalAttrs: _prevAttrs: {
      version = "1.26.5";
      src = final.fetchurl {
        url = "https://go.dev/dl/go${finalAttrs.version}.src.tar.gz";
        hash = "sha256-SVvkvIcXasVnOS5bQRar2YRm0z17SdQedkzMaXay3EI=";
      };
    }
  );
}
