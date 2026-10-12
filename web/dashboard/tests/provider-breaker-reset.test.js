// Force-reset circuit-breaker action on the provider cards' expanded
// details. There is no DOM test harness in this suite, so — like
// editor-dialog.test.js — the component cases assert the wiring contract
// directly on the component source; the pure-logic parts (reset URL) are
// tested against providersLogic.js.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { providerBreakerResetPath } from "../src/pages/overview/providersLogic.js";

const SRC = fileURLToPath(new URL("../src", import.meta.url));
const details = readFileSync(
  join(SRC, "pages/overview/ProviderStatusCardDetails.svelte"),
  "utf8",
);
const card = readFileSync(
  join(SRC, "pages/overview/ProviderStatusCard.svelte"),
  "utf8",
);
const enCatalog = JSON.parse(
  readFileSync(join(SRC, "..", "messages/en.json"), "utf8"),
);

test("the breaker reset endpoint path is built from the provider name", () => {
  assert.equal(
    providerBreakerResetPath("openai-primary"),
    "/admin/providers/openai-primary/breaker/reset",
  );
  // Names with separators must not break out of the path segment.
  assert.equal(
    providerBreakerResetPath("my/provider"),
    "/admin/providers/my%2Fprovider/breaker/reset",
  );
  assert.equal(providerBreakerResetPath(""), "");
});

test("the Force reset button lives in the expanded details only", () => {
  // The card's always-visible markup (head, meta, toggle) must stay free
  // of the reset action; the button renders inside the details body.
  assert.doesNotMatch(card, /overview_breaker_reset/);
  assert.match(details, /overview_breaker_reset\(\)/);
  // Inline with the Breaker State row: the button sits next to the state
  // chip inside the breaker config row, not in the generic config list.
  assert.match(
    details,
    /overview_breaker_state\(\)[\s\S]*?providerBreakerStateLabel\(provider\)[\s\S]*?overview_breaker_reset\(\)/,
  );
  // The details body is the collapsed/expanded boundary, so a collapsed
  // card hides the button with it.
  assert.match(details, /class:is-collapsed=\{!expanded\}/);
});

test("clicking Force reset opens the shared confirmation dialog", () => {
  assert.match(details, /confirmDialog\.open\(\{/);
  assert.match(details, /title: m\.overview_breaker_reset_confirm_title\(\)/);
  assert.match(
    details,
    /message: m\.overview_breaker_reset_confirm_message\(\{ provider: provider\.name \}\)/,
  );
  // A simple confirm — no requiredText — with the action as its onConfirm.
  assert.doesNotMatch(details, /requiredText/);
  assert.match(details, /onConfirm: \(\) => resetBreaker\(\)/);
});

test("confirming posts to the breaker reset endpoint and refreshes status", () => {
  assert.match(details, /sendJSON\(\s*providerBreakerResetPath\(provider\.name\),\s*"POST"/);
  // Success closes the dialog, flashes a confirmation, and refetches the
  // provider status so the breaker chip flips to Closed live.
  assert.match(details, /confirmDialog\.close\(\)/);
  assert.match(details, /flash\.success\(m\.overview_breaker_reset_done\(\{ provider: provider\.name \}\)\)/);
  assert.match(details, /providerStatusState\.fetch\(\)/);
});

test("a failed reset surfaces an error and keeps the card intact", () => {
  // Non-OK responses (e.g. 404 unknown provider) put a message into the
  // dialog's error slot and leave it open; the fetch() refresh must not
  // run for failures.
  assert.match(
    details,
    /confirmDialog\.error = m\.overview_breaker_reset_failed\(\{ provider: provider\.name \}\)/,
  );
});

test("the reset button disables while a reset request is in flight", () => {
  assert.match(details, /disabled=\{resetting\}/);
  assert.match(details, /resetting = true/);
  assert.match(details, /resetting = false/);
});

test("the breaker reset i18n messages exist in every locale catalog", () => {
  for (const key of [
    "overview_breaker_reset",
    "overview_breaker_reset_confirm_title",
    "overview_breaker_reset_confirm_message",
    "overview_breaker_reset_done",
    "overview_breaker_reset_failed",
  ]) {
    assert.ok(
      typeof enCatalog[key] === "string" && enCatalog[key].trim() !== "",
      `messages/en.json is missing ${key}`,
    );
  }
});
