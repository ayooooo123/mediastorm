# Archived Iroh iOS smoke scaffold

This historical experiment only proved Rust static-library linking through a small C ABI. It is not the production client and is not used by current builds.

The maintained client is `modules/iroh-bridge/` in the [frontend repository](https://github.com/godver3-org/mediastorm-frontend), available at `frontend/modules/iroh-bridge/` in a combined local checkout. Its Rust crate and native build scripts support iOS, tvOS, and Android.

Historical smoke-build commands:

```sh
cargo build --locked --target aarch64-apple-ios
cargo build --locked --target aarch64-apple-ios-sim
```
