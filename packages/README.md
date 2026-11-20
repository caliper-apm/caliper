# Client SDKs & CLI (`packages/`)

Native-First architecture:
- `sdk-core/` — Language-neutral state-machine specs & golden test vectors (`fixtures/contract-vectors.json`)
- `sdk-android/` — Pure Native Android SDK (Kotlin, zero React Native dependency)
- `sdk-ios/` — Pure Native iOS SDK (Swift Package Manager, zero React Native dependency)
- `sdk-react-native/` — Optional first-class React Native adapter (Fabric / TurboModule / Hermes)
- `cli-symbols/` — Build identity registration & symbol upload CLI (R8, dSYM, Hermes SourceMap)
