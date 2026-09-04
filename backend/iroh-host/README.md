# Iroh remote-access host

Production Rust host for MediaStorm remote access. The Go service in
`backend/services/remoteaccess/` launches this process to proxy Iroh connections
to the backend and publish pairing-code rendezvous records.

## Build and test

From the repository root:

```sh
cargo test --locked --manifest-path backend/iroh-host/Cargo.toml
cargo build --release --locked --manifest-path backend/iroh-host/Cargo.toml
backend/iroh-host/target/release/iroh-direct-spike --help
```

The package and executable retain the historical name `iroh-direct-spike` for
compatibility. Protocol identifiers, invite formats, and command-line flags are
unchanged by the source relocation.

## Runtime integration

The Go backend discovers this directory when launched from the repository root
or `backend/`. `MEDIASTORM_IROH_DIRECT_DIR` overrides discovery; update any local
source-directory override to this directory. Prebuilt release, debug, and bare
directory binaries are preferred in that order, with `cargo run` as fallback.
Rebuild after source changes so an old release binary does not mask them.

Docker builds from the repository root with `-f backend/Dockerfile`, compiles
this crate in the `iroh-builder` stage, and installs the executable at
`/opt/iroh/iroh-direct-spike`. The runtime environment override remains
`MEDIASTORM_IROH_DIRECT_DIR=/opt/iroh`; Cargo is not required in the final image.

The backend supplies `--origin`, `--secret-file`, and `--rendezvous-file` as
configured. Preserve the host secret file to retain identity across restarts.
For manual command options, run `iroh-direct-spike host --help`. Local startup
scripts must build from this directory and use its `target/release` executable.

The production native client lives in `frontend/modules/iroh-bridge/` in the
separate frontend repository. `archive/iroh-ios-bridge/` is only a historical
linking smoke test.
