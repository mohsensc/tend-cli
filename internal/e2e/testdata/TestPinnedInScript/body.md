## protoc 25.1 → 36.2

**Why**: breaking · major release · released 2026-09-17
**Level**: major (25 → 36); release notes below mention breaking changes in 4 of 12 releases

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
- **v28.0 Protocol Buffers v28.0** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v28.0)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Compiler
  * UPB text encoder without using reflection for Rust (used for a message's Debug trait) that will print out field number to value entries instead of field name to value entries of a message like how it's expected for the usual text format using reflection. (https://github.com/protocolbuffers/protobuf/commit/f9dd9ce66e05d034378b4b37b82307f41fc3395c)
  * Enable MessageLite::DebugString to use Message::DebugString where possible. (https://github.com/protocolbuffers/protobuf/commit/f810cc5b668d78cb982f44b34f188160d55c27d6)
  * Always report plugin support errors from protoc. (https://github.com/protocolbuffers/protobuf/commit/986ef8d80bb9432ec7c3a83f0988e2cc589d5cee)
  …
  ```
- **v27.0 Protocol Buffers v27.0** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v27.0)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # Compiler
  * Future-proof edition 2023 against feature value support windows. (https://github.com/protocolbuffers/protobuf/commit/fea6847e408d58e40a2f10ab124ae795afd703d4)
  * Mark deleted fields in `descriptor.proto` reserved (https://github.com/protocolbuffers/protobuf/commit/d2da4636c06ccad04371434dabbf24ca1c6ee83e)
  * Add extension declarations for known features. (https://github.com/protocolbuffers/protobuf/commit/f8d0a5b1dd6f5655be3c0d1a8aa1fec88e80c13a)
  …
  ```
- **v26.0 Protocol Buffers v26.0** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v26.0)
  ```
  # Announcements
  * **This version includes breaking changes to: Java, PHP, C++, Ruby, Python.**
    * [Java] Remove the deprecated always_print_primitive_fields option from Java, Python and C++ JSON parsers. (https://github.com/protocolbuffers/protobuf/commit/06e7caba58ede0220b110b89d08f329e5f8a7537)
    * [Java] Remove unused helper methods for creating new mutable arraylists. (https://github.com/protocolbuffers/protobuf/commit/787447430fc9a69c071393e85a380b664d261ab4)
    * [Java] Use Editions features in Java full runtimes. (https://github.com/protocolbuffers/protobuf/commit/65c65c2d04b293225db150029d005056a9f078b8)
    * [Java] Stop generating the "newInstance" method. This is not used anymore. (https://github.com/protocolbuffers/protobuf/commit/c0d08bdcade9d44698015b85c9c23343ca1319b7)
  …
  ```
- **v25.2 Protocol Buffers v25.2** · [full notes](https://github.com/protocolbuffers/protobuf/releases/tag/v25.2)
  ```
  # Announcements
  * [Protobuf News](https://protobuf.dev/news/) may include additional announcements or pre-announcements for upcoming changes.
  # C++
  * Only substitute prefixes during installation setup. (https://github.com/protocolbuffers/protobuf/commit/05ad6525c3aae2ae5b6e1987227445a420caada7)
  * Register a shutdown delete for C++ feature defaults (https://github.com/protocolbuffers/protobuf/commit/3d5c709ebe01f1f9f3d3345cd4239e239081f8fd)
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
