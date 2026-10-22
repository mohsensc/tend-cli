## protoc 25.1 → 36.2

**Why**: breaking · major release · released 2026-09-17
**Level**: major (25 → 36); release notes below mention breaking changes in 3 of 8 releases

### What changed upstream

- **v36.2 Protocol Buffers v36.2** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Compiler
  * Detects single-crate cross-file symbol collisions and conditionally emits crate-root re-exports. (https://github.com/protocolbuffers/protobuf/commit/30780158a07394a22eba3d57527ef32cac795e76)
  * Prepend the file module name to Rust type paths to use canonical paths. (https://github.com/protocolbuffers/protobuf/commit/7390c85c5ce3707ab236aeb511eeeaba49c2f4fa)
  * Expose generated per-file Rust proto modules as public. (https://github.com/protocolbuffers/protobuf/commit/7ed8a4bd72ba92b741fae08d6f05cbf987b7bcf1)
  …
  ```
- **v36.1 Protocol Buffers v36.1** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v36.1)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Csharp
  * Allow later enum aliases to overwrite in C# custom JSON enum mapping. (https://github.com/protocolbuffers/protobuf/commit/cc7e6bd557dc9a9d4b984314d6456f2a3ad78570)
  # Rust
  * Update README files for each crate. (https://github.com/protocolbuffers/protobuf/commit/e64a176925c0b1978bd6d14853605df384d60493)
  …
  ```
- **v36.0 Protocol Buffers v36.0** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v36.0)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  * [Migration Guide](https://protobuf.dev/support/migration/) may include additional guidance for breaking changes.
  # Bazel
  * Add Bazel9 test (https://github.com/protocolbuffers/protobuf/commit/90ad7e978849e11406eeb47baf228b099c5da9d0)
  * Bazel 9 tests for csharp, hpb, objc, php, python, rust and upb (https://github.com/protocolbuffers/protobuf/commit/f9e028f29fa83d0fcfb01c19ce0a4635e9beff5c)
  …
  ```
- **v35.1 Protocol Buffers v35.1** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v35.1)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Bazel
  * Bazel 9 tests for csharp, hpb, objc, php, python, rust and upb (#27598) (https://github.com/protocolbuffers/protobuf/commit/4469e3840bfa69ee3bd21721b402ff28076ecbab)
  * Break protobuf dependency on Bazel's proto fragment. Only respect the Starlark versions of --proto_toolchain_for*. This is a breaking change from 35.0, but matches the behavior in 34.x. (https://github.com/protocolbuffers/protobuf/commit/1f99c52d771b86ca11274019c9b1a10280dd6050)
  # C++
  …
  ```
- **v35.0 Protocol Buffers v35.0** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v35.0)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Bazel
  * Bazel 9 Support: Upgrade tests to Bazel 9 (https://github.com/protocolbuffers/protobuf/commit/3ee8d8a008a5696f61aa72327e2c4cf2ae068e00)
  * Moved `protocopt` flag out of the `cc` dir, since it is not a c++-specific flag. (https://github.com/protocolbuffers/protobuf/commit/325d8dca2ffdc658824aa6b05d825dcb86cca2cb)
  * Add support for bazel 9.x (#26201) (https://github.com/protocolbuffers/protobuf/commit/f08d70329211dfd74584c3761da5da5e32ab34f3)
  …
  ```
- **v34.2 Protocol Buffers v34.2** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v34.2)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Bazel
  * Remove unused dependency on aspect_bazel_lib (https://github.com/protocolbuffers/protobuf/commit/784a77f6e7705df26380980927623c38d67990be)
  # C++
  * Workaround for attribute handling bug in gcc < 13 (https://github.com/protocolbuffers/protobuf/commit/1a5c5c5d8a2292e73e82f8479e87266008b910c4)
  …
  ```
- **v34.1 Protocol Buffers v34.1** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v34.1)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Bazel
  * Moved `protocopt` flag out of the `cc` dir, since it is not a c++-specific flag. (https://github.com/protocolbuffers/protobuf/commit/3db9fd1be16917f26590eccd6dfb51b8dcad0d15)
  * Add support for bazel 9.x (#26201) (https://github.com/protocolbuffers/protobuf/commit/763c5da3ff011a49f1c00992cdf9b4924d6b74bb)
  # C++
  …
  ```
- **v34.0 Protocol Buffers v34.0** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v34.0)
  ```
  # Announcements
  * **This version includes potential breaking changes with major version bump to: Objective-C, Python, PHP, C++. Bazel**
    * [Objective-C] Remove `-[GPBFieldDescriptor optional]` (https://github.com/protocolbuffers/protobuf/commit/3414dc151eb4dcbdb2ca952e2589993bf7af75c4)
    * [Objective-C] Fix nullability annotations on some `GPB*Dictionary` types. (https://github.com/protocolbuffers/protobuf/commit/ea67d6d26a48478a567c404679e3bb99cf230d50)
    * [Objective-C] Remove `generate_minimal_imports` generation option warning (https://github.com/protocolbuffers/protobuf/commit/45b1297fdaad5a9436d0e207422168c38dc45ac4)
    * [Python] Raise errors in OSS when assign bool to int/enum field in Python Proto. (https://github.com/protocolbuffers/protobuf/commit/5b116fe2f14f49dd0cc3b76089983717f211025c)
  …
  ```

### Touched in this repo

- `scripts/install.sh:5` `PROTOC_VERSION=25.1` → `PROTOC_VERSION=36.2`

### Proof

No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run.

### Vetting

- scope: 1 file, explained by this change
- question: not run: no model configured
- not applicable: symbols, fields, leftovers, lockfile, debug, comments, dead

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="custom/protoc@36.2" base="BASE" rev="1" -->
