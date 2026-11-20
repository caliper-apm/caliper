# Contributing to Caliper APM

Thank you for contributing to **Caliper APM**! Before opening a PR, please review [`NORTH_STAR.md`](NORTH_STAR.md) and [`AGENTS.md`](AGENTS.md).

## Core Engineering Rules

1. **Strict Clean-Room Purity**: All code, schemas, UI components, and fixtures must be original open-source work. Never commit credentials, private domains, internal brand names, or personal paths. Run `./scripts/cleanroom-guard.sh` before committing.
2. **Native-First Separation**: `packages/sdk-android` and `packages/sdk-ios` MUST remain 100% free of React Native or JavaScript dependencies.
3. **Golden Contract Parity**: Any change to sampling (FNV-1a), 19-bucket histograms, zero-frame fluency classification, or sliding 24h quotas MUST update `packages/sdk-core/fixtures/contract-vectors.json` and pass across Go, Kotlin, Swift, and TypeScript.
4. **Zero-Lie Analytics & Real-Data Console**: Never zero-fill missing observations, never average daily P90 quantiles, and never add mock-only placeholder pages to `console/`.

## Local Verification Checklist

```bash
./scripts/cleanroom-guard.sh
(cd server && go vet ./... && go test -race ./...)
(cd console && npm test && npx tsc --noEmit && npm run build)
(cd packages/sdk-react-native && node --test tests/*.test.ts)
(cd packages/sdk-ios && swift test)
(cd packages/sdk-android && gradle :caliper-core:test)
(cd packages/gradle-plugin && gradle test)
ruby packages/fastlane-plugin/test/uploader_test.rb
./demo-app/chaos-lab/run-chaos-e2e.sh
```
