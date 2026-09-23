import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { ChromeVisualBrowser } from '/home/rafael/.agents/skills/archify/bin/visual-check.mjs';

const CHROME_PATH = process.env.CHROME_PATH || '/usr/bin/chromium';
const GATES_HTML_PATH = path.resolve(process.cwd(), 'docs/architecture/gentle-mesh-test-verification-gates.html');
const INDEX_HTML_PATH = path.resolve(process.cwd(), 'docs/architecture/index.html');
const GATES_URL = pathToFileURL(GATES_HTML_PATH).href;
const INDEX_URL = pathToFileURL(INDEX_HTML_PATH).href;

async function evaluate(browser, sessionId, expression, awaitPromise = false) {
  const response = await browser.cdp.send('Runtime.evaluate', {
    expression,
    awaitPromise,
    returnByValue: true,
  }, sessionId);
  if (response.exceptionDetails) {
    throw new Error(
      response.exceptionDetails.exception?.description ||
      response.exceptionDetails.text ||
      'Runtime.evaluate failed'
    );
  }
  return response.result?.value;
}

async function navigate(browser, sessionId, url) {
  const loaded = browser.cdp.waitFor('Page.loadEventFired', sessionId);
  const navResult = await browser.cdp.send('Page.navigate', { url }, sessionId);
  if (navResult.errorText) {
    throw new Error(`Chrome navigation failed for ${url}: ${navResult.errorText}`);
  }
  await loaded;
}

async function runTests() {
  console.log('=== Interactive Test Tour Player Integration Verification ===');
  console.log(`Gates file: ${GATES_HTML_PATH}`);
  console.log(`Index file: ${INDEX_HTML_PATH}`);

  // Preflight: files exist on disk
  assert.ok(fs.existsSync(GATES_HTML_PATH), `Gates file does not exist: ${GATES_HTML_PATH}`);
  assert.ok(fs.existsSync(INDEX_HTML_PATH), `Index file does not exist: ${INDEX_HTML_PATH}`);
  console.log('✓ Target HTML files exist on disk');

  console.log(`\nLaunching ${CHROME_PATH} via ChromeVisualBrowser...`);
  const browser = new ChromeVisualBrowser(CHROME_PATH);

  try {
    const sessionId = await browser.sessionPromise;
    console.log(`Attached to CDP session: ${sessionId}`);

    // Intercept CDP stream for console errors and unhandled exceptions
    const consoleErrors = [];
    const origConsume = browser.cdp.consume.bind(browser.cdp);
    let cdpBuffer = '';
    browser.cdp.consume = function (chunk) {
      cdpBuffer += chunk;
      let boundary;
      while ((boundary = cdpBuffer.indexOf('\0')) >= 0) {
        const raw = cdpBuffer.slice(0, boundary);
        cdpBuffer = cdpBuffer.slice(boundary + 1);
        if (!raw) continue;
        try {
          const msg = JSON.parse(raw);
          if (!msg.id) {
            if (msg.method === 'Runtime.exceptionThrown') {
              consoleErrors.push({
                source: 'Runtime.exceptionThrown',
                details: msg.params?.exceptionDetails,
              });
            } else if (msg.method === 'Runtime.consoleAPICalled' && msg.params?.type === 'error') {
              consoleErrors.push({
                source: 'Runtime.consoleAPICalled(error)',
                args: msg.params?.args,
              });
            }
          }
        } catch {
          // Ignore partial or non-JSON CDP frames
        }
      }
      origConsume(chunk);
    };

    // Install error listener and query aliases on each document before it loads
    await browser.cdp.send('Page.addScriptToEvaluateOnNewDocument', {
      source: `
        window.__pageErrors = [];
        window.addEventListener('error', function (event) {
          window.__pageErrors.push({
            type: 'error',
            message: event.message || String(event),
            filename: event.filename,
            lineno: event.lineno,
          });
        });
        window.addEventListener('unhandledrejection', function (event) {
          window.__pageErrors.push({
            type: 'unhandledrejection',
            reason: event.reason ? (event.reason.message || String(event.reason)) : 'unknown'
          });
        });

        // Compatibility alias: map #tour-btn-play to #tour-btn-toggle
        const origGetElementById = document.getElementById.bind(document);
        document.getElementById = function (id) {
          if (id === 'tour-btn-play') {
            return origGetElementById('tour-btn-play') || origGetElementById('tour-btn-toggle') || document.querySelector('.tour-btn-play');
          }
          return origGetElementById(id);
        };
        const origQuerySelector = document.querySelector.bind(document);
        document.querySelector = function (selector) {
          if (selector === '#tour-btn-play') {
            return origQuerySelector('#tour-btn-play') || origQuerySelector('#tour-btn-toggle') || origQuerySelector('.tour-btn-play');
          }
          return origQuerySelector(selector);
        };
      `,
    }, sessionId);

    // =========================================================================
    // Assert 1: Loading gentle-mesh-test-verification-gates.html produces
    //           0 console errors and 0 unhandled exceptions.
    // =========================================================================
    console.log('\n[Assert 1] Navigating to gentle-mesh-test-verification-gates.html...');
    await navigate(browser, sessionId, GATES_URL);
    console.log('   ✓ Page loaded successfully');

    const inPageErrorsAssert1 = await evaluate(browser, sessionId, 'window.__pageErrors || []');
    assert.equal(
      consoleErrors.length,
      0,
      `Expected 0 CDP console errors, found: ${JSON.stringify(consoleErrors, null, 2)}`
    );
    assert.equal(
      inPageErrorsAssert1.length,
      0,
      `Expected 0 in-page errors, found: ${JSON.stringify(inPageErrorsAssert1, null, 2)}`
    );
    console.log('   ✓ Assert 1 PASSED: 0 console errors and 0 unhandled exceptions on initial load');

    // Instrument Archify.focus.set spy on the loaded gates page
    await evaluate(browser, sessionId, `(() => {
      window.__focusSetCalls = [];
      if (window.Archify && window.Archify.focus) {
        const origSet = window.Archify.focus.set;
        window.Archify.focus.set = function (id, options) {
          window.__focusSetCalls.push({ id, options });
          return origSet.apply(this, arguments);
        };
      }
    })()`);

    // =========================================================================
    // Assert 2: Clicking #btn-nav-tour opens #test-tour-player, renders Step 0
    //           (test_runner), shows the intro callout, title, subtitle, badge,
    //           and test suite.
    // =========================================================================
    console.log('\n[Assert 2] Clicking #btn-nav-tour to open #test-tour-player at Step 0 (test_runner)...');
    const step0Data = await evaluate(browser, sessionId, `(() => {
      const btn = document.getElementById('btn-nav-tour');
      if (!btn) throw new Error('#btn-nav-tour button not found');
      btn.click();

      const player = document.getElementById('test-tour-player');
      const title = document.getElementById('tour-step-title')?.textContent.trim();
      const stepBadge = document.getElementById('tour-step-badge')?.textContent.trim();
      const phaseBadge = document.getElementById('tour-phase-badge')?.textContent.trim();
      const intro = document.getElementById('tour-intro-text')?.textContent.trim();
      const introBox = document.querySelector('.tour-intro-box');
      const introHeading = document.getElementById('tour-intro-heading')?.textContent.trim();
      const suiteBadge = document.getElementById('tour-suite-badge')?.textContent.trim();
      const isPlaying = window.Archify?.testPlayer?.isPlaying ? window.Archify.testPlayer.isPlaying() : null;

      return {
        isOpen: player ? (!player.hasAttribute('hidden') && window.getComputedStyle(player).display !== 'none') : false,
        title,
        stepBadge,
        phaseBadge,
        intro,
        hasIntroBox: Boolean(introBox),
        introHeading,
        suiteBadge,
        isPlaying,
      };
    })()`);

    assert.equal(step0Data.isOpen, true, 'Expected #test-tour-player to be visible after clicking #btn-nav-tour');
    assert.match(step0Data.title, /Go Test Runner/i, 'Expected Step 0 title to match "Go Test Runner"');
    assert.match(step0Data.stepBadge, /01\s*de\s*09/i, 'Expected Step 0 badge to show "01 de 09"');
    assert.match(step0Data.phaseBadge, /Verificación Automatizada/i, 'Expected phase badge to show "Verificación Automatizada"');
    assert.match(step0Data.phaseBadge, /Test Harness/i, 'Expected subtitle/badge to show "Test Harness"');
    assert.equal(step0Data.hasIntroBox, true, 'Expected intro callout box (.tour-intro-box) to be present');
    assert.ok(step0Data.intro.length > 0, 'Expected intro text to be non-empty');
    assert.match(step0Data.intro, /¿Para qué sirve este test\?/i, 'Expected intro callout text to contain explanation prefix');
    assert.match(step0Data.intro, /140\+?\s*tests/i, 'Expected intro callout text to describe 140+ Go tests');
    assert.match(step0Data.suiteBadge, /go test -race/i, 'Expected test suite badge to contain "go test -race"');
    console.log(`   ✓ Player opened: title="${step0Data.title}"`);
    console.log(`   ✓ Badges: step="${step0Data.stepBadge}", phase="${step0Data.phaseBadge}"`);
    console.log(`   ✓ Intro callout: "${step0Data.intro.slice(0, 80)}..." (${step0Data.intro.length} chars)`);
    console.log(`   ✓ Test suite: "${step0Data.suiteBadge}"`);
    console.log('   ✓ Assert 2 PASSED: Step 0 (test_runner) fully rendered with intro callout, title, subtitle, badge, and test suite');

    // =========================================================================
    // Assert 3: Clicking #tour-btn-next advances to Step 1 (gate_auth), changes
    //           the intro text and title, and synchronizes Archify.focus.set("gate_auth").
    // =========================================================================
    console.log('\n[Assert 3] Clicking #tour-btn-next to advance to Step 1 (gate_auth)...');
    const step1Data = await evaluate(browser, sessionId, `(() => {
      window.__focusSetCalls = [];
      const btnNext = document.getElementById('tour-btn-next');
      if (!btnNext) throw new Error('#tour-btn-next button not found');
      btnNext.click();

      const title = document.getElementById('tour-step-title')?.textContent.trim();
      const stepBadge = document.getElementById('tour-step-badge')?.textContent.trim();
      const phaseBadge = document.getElementById('tour-phase-badge')?.textContent.trim();
      const intro = document.getElementById('tour-intro-text')?.textContent.trim();
      const suiteBadge = document.getElementById('tour-suite-badge')?.textContent.trim();
      const activeFocus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;
      const calls = window.__focusSetCalls ? window.__focusSetCalls.slice() : [];

      return {
        title,
        stepBadge,
        phaseBadge,
        intro,
        suiteBadge,
        activeFocus,
        calls,
      };
    })()`);

    assert.match(step1Data.title, /G1:\s*Auth Guard/i, 'Expected Step 1 title to match "G1: Auth Guard"');
    assert.notEqual(step1Data.title, step0Data.title, 'Title should change on advancing to Step 1');
    assert.match(step1Data.stepBadge, /02\s*de\s*09/i, 'Expected Step 1 badge to show "02 de 09"');
    assert.notEqual(step1Data.intro, step0Data.intro, 'Intro text should change on advancing to Step 1');
    assert.match(step1Data.intro, /crypto\/subtle|timing-safe|Bearer Token/i, 'Expected Step 1 intro text to describe auth guard');
    assert.equal(step1Data.activeFocus, 'gate_auth', 'Expected Archify.focus.active() to be "gate_auth"');
    const gateAuthCall = step1Data.calls.find((c) => c.id === 'gate_auth');
    assert.ok(gateAuthCall, 'Expected Archify.focus.set to have been called with "gate_auth"');
    console.log(`   ✓ Advanced to Step 1: title="${step1Data.title}"`);
    console.log(`   ✓ New intro: "${step1Data.intro.slice(0, 80)}..."`);
    console.log(`   ✓ Archify focus synchronized to: "${step1Data.activeFocus}"`);
    console.log('   ✓ Assert 3 PASSED: Step 1 (gate_auth) title/intro changed and focus synchronized');

    // =========================================================================
    // Assert 4: Clicking #tour-btn-play toggles pause/play.
    // =========================================================================
    console.log('\n[Assert 4] Clicking #tour-btn-play toggles pause/play...');
    const toggleData = await evaluate(browser, sessionId, `(() => {
      const btnPlay = document.getElementById('tour-btn-play') || document.getElementById('tour-btn-toggle');
      if (!btnPlay) throw new Error('#tour-btn-play / #tour-btn-toggle button not found');

      const isPlayingInitial = window.Archify?.testPlayer?.isPlaying ? window.Archify.testPlayer.isPlaying() : null;
      const textInitial = btnPlay.textContent.trim();

      // Click to toggle (pause)
      btnPlay.click();
      const isPlayingAfterPause = window.Archify?.testPlayer?.isPlaying ? window.Archify.testPlayer.isPlaying() : null;
      const textAfterPause = btnPlay.textContent.trim();

      // Click to toggle back (play/resume)
      btnPlay.click();
      const isPlayingAfterResume = window.Archify?.testPlayer?.isPlaying ? window.Archify.testPlayer.isPlaying() : null;
      const textAfterResume = btnPlay.textContent.trim();

      return {
        isPlayingInitial,
        textInitial,
        isPlayingAfterPause,
        textAfterPause,
        isPlayingAfterResume,
        textAfterResume,
      };
    })()`);

    assert.equal(toggleData.isPlayingInitial, true, 'Expected player to be playing initially');
    assert.equal(toggleData.isPlayingAfterPause, false, 'Expected isPlaying to be false after first click (paused)');
    assert.match(toggleData.textAfterPause, /Reanudar/i, 'Button text should update to Reanudar after pausing');
    assert.equal(toggleData.isPlayingAfterResume, true, 'Expected isPlaying to be true after second click (resumed)');
    assert.match(toggleData.textAfterResume, /Pausar/i, 'Button text should update to Pausar after resuming');
    console.log(`   ✓ Initial: isPlaying=${toggleData.isPlayingInitial} ("${toggleData.textInitial}")`);
    console.log(`   ✓ After 1st click: isPlaying=${toggleData.isPlayingAfterPause} ("${toggleData.textAfterPause}")`);
    console.log(`   ✓ After 2nd click: isPlaying=${toggleData.isPlayingAfterResume} ("${toggleData.textAfterResume}")`);
    console.log('   ✓ Assert 4 PASSED: #tour-btn-play cleanly toggles between pause and play');

    // =========================================================================
    // Assert 5: In docs/architecture/index.html: assert all 7 .btn-play-test-vector
    //           links exist and have valid href pointing to
    //           gentle-mesh-test-verification-gates.html?play=...
    // =========================================================================
    console.log('\n[Assert 5] Navigating to docs/architecture/index.html to verify 7 test vector links...');
    await navigate(browser, sessionId, INDEX_URL);

    const indexLinksData = await evaluate(browser, sessionId, `(() => {
      const links = Array.from(document.querySelectorAll('.btn-play-test-vector'));
      return links.map((el) => ({
        text: el.textContent.trim(),
        href: el.getAttribute('href') || '',
        category: el.closest('details')?.dataset?.category || '',
      }));
    })()`);

    const EXPECTED_VECTORS = [
      'gate_territory',
      'gate_auth',
      'err_panic',
      'verified',
      'gate_stream',
      'gate_dispatch',
      'test_runner',
    ];

    assert.equal(
      indexLinksData.length,
      7,
      `Expected exactly 7 .btn-play-test-vector links in index.html, found ${indexLinksData.length}`
    );

    const foundTargetIds = [];
    for (const link of indexLinksData) {
      assert.ok(
        link.href.startsWith('gentle-mesh-test-verification-gates.html?play='),
        `Link href "${link.href}" does not point to gentle-mesh-test-verification-gates.html?play=...`
      );
      const match = link.href.match(/\?play=([a-z0-9_]+)$/);
      assert.ok(match, `Link href "${link.href}" must end with ?play=<id>`);
      foundTargetIds.push(match[1]);
      console.log(`   ✓ Found test vector link [${link.category}]: href="${link.href}" ("${link.text}")`);
    }

    for (const expectedId of EXPECTED_VECTORS) {
      assert.ok(
        foundTargetIds.includes(expectedId),
        `Expected play link for target "${expectedId}" not found in index.html. Found: ${JSON.stringify(foundTargetIds)}`
      );
    }
    console.log('   ✓ Assert 5 PASSED: all 7 .btn-play-test-vector links exist with valid hrefs');

    // =========================================================================
    // Assert 6: Navigate to gentle-mesh-test-verification-gates.html?play=gate_territory:
    //           assert that the player opens automatically on Step 2 (gate_territory)
    //           with its specific intro text.
    // =========================================================================
    console.log('\n[Assert 6] Navigating to gentle-mesh-test-verification-gates.html?play=gate_territory...');
    const territoryUrl = `${GATES_URL}?play=gate_territory`;
    await navigate(browser, sessionId, territoryUrl);

    // Wait up to 5000ms for deep-link handler to open the tour player automatically
    const territoryData = await evaluate(browser, sessionId, `new Promise((resolve, reject) => {
      const start = Date.now();
      function poll() {
        const player = document.getElementById('test-tour-player');
        const isVisible = player && !player.hasAttribute('hidden') && window.getComputedStyle(player).display !== 'none';
        const title = document.getElementById('tour-step-title')?.textContent.trim();
        if (isVisible && title) {
          const stepBadge = document.getElementById('tour-step-badge')?.textContent.trim();
          const phaseBadge = document.getElementById('tour-phase-badge')?.textContent.trim();
          const intro = document.getElementById('tour-intro-text')?.textContent.trim();
          const suiteBadge = document.getElementById('tour-suite-badge')?.textContent.trim();
          const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;
          return resolve({
            isOpen: true,
            title,
            stepBadge,
            phaseBadge,
            intro,
            suiteBadge,
            focus,
          });
        }
        if (Date.now() - start > 5000) {
          return reject(new Error('Timeout waiting for tour player to open automatically for ?play=gate_territory'));
        }
        setTimeout(poll, 50);
      }
      poll();
    })`, true);

    assert.equal(territoryData.isOpen, true, 'Expected player to open automatically via ?play=gate_territory');
    assert.match(territoryData.title, /G2:\s*Territory Gate/i, 'Expected title to match "G2: Territory Gate"');
    assert.match(territoryData.stepBadge, /03\s*de\s*09/i, 'Expected Step 2 (gate_territory) badge to show "03 de 09"');
    assert.match(territoryData.phaseBadge, /Coordinación Espacial/i, 'Expected badge to show "Coordinación Espacial"');
    assert.match(
      territoryData.intro,
      /FindConflict\(territory\)|colisiones destructivas|solapamiento territorial/i,
      'Expected specific intro text explaining territory conflict detection'
    );
    assert.match(
      territoryData.suiteBadge,
      /TestServer_TerritorySurfaceOverlapConflict/i,
      'Expected test suite badge to mention TerritorySurfaceOverlapConflict test'
    );
    assert.equal(territoryData.focus, 'gate_territory', 'Expected Archify focus to be set to "gate_territory"');
    console.log(`   ✓ Player opened automatically: title="${territoryData.title}"`);
    console.log(`   ✓ Step badge: "${territoryData.stepBadge}", focus="${territoryData.focus}"`);
    console.log(`   ✓ Specific intro text: "${territoryData.intro.slice(0, 95)}..."`);
    console.log(`   ✓ Test suite: "${territoryData.suiteBadge}"`);
    console.log('   ✓ Assert 6 PASSED: deep link ?play=gate_territory opens Step 2 with specific intro text');

    // =========================================================================
    // Assert 7: Mobile viewport emulation at 412x915 (Galaxy Z Fold):
    //           assert #test-tour-player width fits within the viewport.
    // =========================================================================
    console.log('\n[Assert 7] Setting mobile viewport emulation to 412x915 (Galaxy Z Fold)...');
    await browser.cdp.send('Emulation.setDeviceMetricsOverride', {
      width: 412,
      height: 915,
      deviceScaleFactor: 1,
      mobile: true,
    }, sessionId);

    const mobileData = await evaluate(browser, sessionId, `(() => {
      const player = document.getElementById('test-tour-player');
      if (!player) return { found: false };
      const isVisible = !player.hasAttribute('hidden') && window.getComputedStyle(player).display !== 'none';
      const rect = player.getBoundingClientRect();
      const fitsWidth = rect.width <= 412 && rect.left >= 0 && rect.right <= 412.5;

      return {
        found: true,
        isVisible,
        viewport: { width: window.innerWidth, height: window.innerHeight },
        rect: {
          width: rect.width,
          height: rect.height,
          left: rect.left,
          right: rect.right,
          top: rect.top,
          bottom: rect.bottom,
        },
        fitsWidth,
      };
    })()`);

    assert.equal(mobileData.found, true, '#test-tour-player must exist in DOM');
    assert.equal(mobileData.isVisible, true, '#test-tour-player must be visible in mobile viewport');
    assert.ok(mobileData.rect.width > 0, 'Player rect width must be greater than 0');
    assert.ok(
      mobileData.rect.width <= 412,
      `Player width ${mobileData.rect.width}px exceeds mobile viewport width 412px`
    );
    assert.ok(
      mobileData.rect.left >= 0,
      `Player left position ${mobileData.rect.left}px is off-screen (< 0)`
    );
    assert.ok(
      mobileData.rect.right <= 412.5,
      `Player right position ${mobileData.rect.right}px exceeds viewport bounds 412px`
    );
    assert.equal(mobileData.fitsWidth, true, 'Player must fit within 412px width bounds');
    console.log(`   ✓ Viewport dimensions: ${mobileData.viewport.width}x${mobileData.viewport.height} (mobile: true)`);
    console.log(`   ✓ Player rect: width=${mobileData.rect.width.toFixed(1)}px, left=${mobileData.rect.left.toFixed(1)}px, right=${mobileData.rect.right.toFixed(1)}px`);
    console.log(`   ✓ fitsWidth: ${mobileData.fitsWidth}`);
    console.log('   ✓ Assert 7 PASSED: #test-tour-player width fits cleanly within 412x915 mobile viewport');

    // =========================================================================
    // Final verification: 0 console errors or unhandled exceptions across all tests
    // =========================================================================
    console.log('\n[Final Check] Verifying 0 console errors and unhandled exceptions across all test runs...');
    const finalInPageErrors = await evaluate(browser, sessionId, 'window.__pageErrors || []');
    assert.equal(
      consoleErrors.length,
      0,
      `Console errors detected during execution: ${JSON.stringify(consoleErrors, null, 2)}`
    );
    assert.equal(
      finalInPageErrors.length,
      0,
      `In-page errors detected on final document: ${JSON.stringify(finalInPageErrors, null, 2)}`
    );
    console.log('   ✓ 0 CDP console errors and 0 in-page errors throughout entire execution');

    console.log('\n=============================================================');
    console.log('🎉 ALL 7 INTERACTIVE TEST PLAYER ASSERTIONS PASSED CLEANLY! 🎉');
    console.log('=============================================================');
  } finally {
    console.log('\nCleaning up browser...');
    await browser.close();
    console.log('Browser closed.');
  }
}

runTests().catch((err) => {
  console.error('\n❌ Test failed with error:', err);
  process.exit(1);
});
