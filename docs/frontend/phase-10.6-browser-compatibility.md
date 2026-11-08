# Phase 10.6 Browser Compatibility

Status: **PASS for all executable gates**
Date: 2026-11-09

## Tested engines

| Browser / engine | Version | Result |
|---|---:|---|
| Playwright Chromium | 141.0.7390.37 | PASS |
| Playwright Firefox | 142.0.1 | PASS |
| Playwright WebKit | 26.0 | PASS |
| Native Safari | 26.5 installed | **NOT VERIFIED** |

Playwright WebKit is reported separately and is not represented as native Safari. SafariDriver reported ready, but Safari refused session creation because **Allow remote automation** is disabled in Safari Settings. The test did not change that user security setting.

## Real cross-engine matrix

All combinations used an isolated real Go server with MySQL 8.4 and Redis 7.4. They covered registration, HttpOnly refresh-cookie bootstrap, document creation and sharing, Monaco input, WebSocket AUTH/JOIN/EDIT, remote cursor and selection decorations, refresh, offline/online reconnect, IndexedDB recovery initialization, and cross-tab logout.

| Combination | Result |
|---|---|
| Chromium + Chromium | PASS |
| Chromium + Firefox | PASS |
| Chromium + WebKit | PASS |
| Firefox + WebKit | PASS |

Execution log: `/tmp/collab-phase104-single-9a0f142e33.log`.

## Compatibility fixes made during acceptance

- The Playwright `channel` setting is now optional so Firefox and WebKit do not inherit the Chrome channel.
- E2E tests focus Monaco through its visible editor surface rather than its engine-specific hidden textarea position.
- Selection assertions allow Monaco to split one logical decoration across token-rendering fragments.
- Unicode cursor conversion explicitly translates between Monaco UTF-16 offsets and protocol code-point offsets.

## Remaining browser boundary

Native Safari is not claimed as passed until remote automation is explicitly enabled and the native Safari smoke/collaboration flow is rerun. Clipboard behavior requiring a user permission prompt is also outside headless automated coverage; editing does not depend on clipboard permission.
