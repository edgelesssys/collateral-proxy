<!--
Copyright 2026 Edgeless Systems GmbH
SPDX-License-Identifier: BUSL-1.1
-->

# collateral-proxy

Read-through caching forward proxy for attestation collateral (AMD KDS, Intel PCS, NVIDIA RIM).

Clients request collateral using the **same paths** the upstream vendors use, but against the proxy's address instead of the vendor host.
The proxy maps the path prefix to the upstream, serves a fresh copy from its cache when it has one, and otherwise fetches, stores, and returns the upstream response.

### Routing

Requests are routed to an upstream by path prefix. Only `GET` is accepted (anything else returns `405`).
Unknown paths return `404`.

| Path prefix           | Upstream host                              | Vendor            |
| --------------------- | ------------------------------------------ | ----------------- |
| `/vcek/`, `/vlek/`    | `kdsintf.amd.com`                          | AMD KDS           |
| `/sgx/`, `/tdx/`      | `api.trustedservices.intel.com`            | Intel PCS         |
| `/IntelSGX…`          | `certificates.trustedservices.intel.com`   | Intel SGX Root CA |
| `/v1/rim/`            | `rim.attestation.nvidia.com`               | NVIDIA RIM        |

For a matched request the proxy reconstructs the upstream URL as `https://<host><path>?<query>`.
The path and query string are passed through unchanged, only the host (and scheme) are set by the proxy.

### Request flow

1. Look up the reconstructed upstream URL in the cache.
2. Fresh cache entry exists: return the cached response directly.
3. Stale cache entry exists: fetch the upstream.
   - On success, the response is returned to the caller.
   - On upstream failure, return stale entry as fallback.
4. No cache entry exists: fetch the upstream.
   - On success, the response is returned to the caller.
   - On upstream failure, return `502 Bad Gateway`.

Relevant response headers are forwarded to the client.
Only `200 OK` responses from upstream are cached.

### Cache & freshness

Each entry's freshness lifetime is computed at store time, in priority order:

1. CRLs: the `nextUpdate` field parsed from the CRL itself.
2. Otherwise the upstream response's `Cache-Control: max-age` (honoring `no-cache` / `no-store` / `must-understand`).
3. Otherwise a default TTL of 1 hour.

### Endpoints

- `GET /healthz`: returns `ok`, use as a readiness probe.
- `GET /metrics`: Prometheus metrics:
  - `collateral_proxy_requests_total{result, document}`:
    - `result` is one of `hit`, `miss`, `stale`, `error`, `rejected`
    - `document` is one of `crl`, `ak-cert`, `collateral`, `unknown`
  - `collateral_proxy_upstream_responses_total{code, document}`: upstream outcomes by HTTP status code (or `error` when the fetch itself failed).

## Usage

### Flags

| Flag                | Default                     | Description                               |
| ------------------- | --------------------------- | ----------------------------------------- |
| `-addr`             | `:80`                       | Listen address.                           |
| `-state-dir`        | `/var/lib/collateral-proxy` | Directory for on-disk cache state.        |
| `-upstream-timeout` | `10s`                       | Per-request timeout for upstream fetches. |

### Running the container

The published image is `ghcr.io/edgelesssys/collateral-proxy:latest`:

```sh
docker run -p 8080:80 -v collateral-proxy-state:/var/lib/collateral-proxy ghcr.io/edgelesssys/collateral-proxy:latest
```

Mount a persistent volume at `-state-dir` so the cache survives restarts.

### Deploying on Kubernetes

Each release attaches a [`collateral-proxy.yaml`](https://github.com/edgelesssys/collateral-proxy/releases/latest/download/collateral-proxy.yml) asset to its [GitHub Release](https://github.com/edgelesssys/collateral-proxy/releases/latest/).

```sh
curl -fLO https://github.com/edgelesssys/collateral-proxy/releases/latest/download/collateral-proxy.yml
kubectl apply -f collateral-proxy.yml
```

### Pointing clients at the proxy

Clients fetch collateral from the proxy using the vendor's own paths. For example, a VCEK certificate normally fetched from

```
https://kdsintf.amd.com/vcek/v1/Milan/<hwid>?blSPL=...&teeSPL=...
```

is instead fetched from the proxy:

```
http://<proxy-host>/vcek/v1/Milan/<hwid>?blSPL=...&teeSPL=...
```

The proxy preserves the path and query and rewrites only the host, so clients only need their collateral base URL repointed at the proxy.

## Development

- Prerequisites: Nix (flakes) and/or a Go toolchain; `direnv`/`.envrc`.
- Build the binary: `nix build .#collateral-proxy`.
- Build the container image: `nix build .#container`.
- Push the image: `nix run .#push -- [tag]` (defaults to the `:dev`).
- Push the image and render the pinned deployment manifest: `nix run .#render-k8s-resources -- [tag]`.
- Format: `nix fmt`.
- Lint: `nix run .#lint`.
- Vuln scan: `nix run .#govulncheck`.
- Run formatters and tests: `nix flake check`.

## Releasing

1. Bump version in `version.txt`.

2. Push to `main`
   ```sh
   git commit -am "release: v0.X.0"
   git push origin release-v0.X.0
   ```

3. Open a PR and merge to `main`.

4. CI running on main publishes `ghcr.io/edgelesssys/collateral-proxy:v0.X.0` and moves `:latest`.

5. Push the `v0.X.0` tag. CI then publishes a GitHub Release and attaches `collateral-proxy.yaml`, the deployment manifest pinned to `v0.X.0@sha256:<digest>`.
