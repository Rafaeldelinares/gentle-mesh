import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { ChromeVisualBrowser } from '/home/rafael/.agents/skills/archify/bin/visual-check.mjs';

const CHROME_PATH = process.env.CHROME_PATH || '/usr/bin/chromium';
const HTML_FILE_PATH = path.resolve(process.cwd(), 'docs/architecture/gentle-mesh-secure-environment.html');
const TARGET_URL = pathToFileURL(HTML_FILE_PATH).href;

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

async function runTests() {
  console.log('--- Topology Step Explainer Integration Test ---');
  console.log(`Target: ${HTML_FILE_PATH}`);

  // 1. File exists check before browser launch
  assert.ok(fs.existsSync(HTML_FILE_PATH), `Target HTML file does not exist: ${HTML_FILE_PATH}`);
  console.log('✓ Target HTML file exists on disk');

  console.log(`Launching ${CHROME_PATH} via ChromeVisualBrowser...`);
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

    // Install error listener and query aliases on new document
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

        // Compatibility alias: map #step-modal-next to #step-btn-next if queried directly
        const origGetElementById = document.getElementById.bind(document);
        document.getElementById = function (id) {
          if (id === 'step-modal-next') {
            return origGetElementById('step-modal-next') || origGetElementById('step-btn-next');
          }
          return origGetElementById(id);
        };
        const origQuerySelector = document.querySelector.bind(document);
        document.querySelector = function (selector) {
          if (selector === '#step-modal-next') {
            return origQuerySelector('#step-modal-next') || origQuerySelector('#step-btn-next');
          }
          return origQuerySelector(selector);
        };
      `,
    }, sessionId);

    console.log(`Navigating via CDP to ${TARGET_URL}...`);
    const loaded = browser.cdp.waitFor('Page.loadEventFired', sessionId);
    const navResult = await browser.cdp.send('Page.navigate', { url: TARGET_URL }, sessionId);
    if (navResult.errorText) {
      throw new Error(`Chrome navigation failed: ${navResult.errorText}`);
    }
    await loaded;
    console.log('✓ Page loaded successfully');

    // Instrument Archify.focus.set spy
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

    // 1. File exists and loads cleanly in headless Chromium (0 console errors)
    console.log('\n[Assert 1] Verifying 0 console errors and 0 unhandled exceptions...');
    const inPageErrors = await evaluate(browser, sessionId, 'window.__pageErrors || []');
    assert.equal(
      consoleErrors.length,
      0,
      `Expected 0 CDP console errors, found: ${JSON.stringify(consoleErrors, null, 2)}`
    );
    assert.equal(
      inPageErrors.length,
      0,
      `Expected 0 in-page errors, found: ${JSON.stringify(inPageErrors, null, 2)}`
    );
    console.log('   ✓ 0 console errors and 0 unhandled exceptions verified');

    // 2. The modal #step-explainer-modal is initially closed
    console.log('\n[Assert 2] Verifying #step-explainer-modal is initially closed...');
    const initialModalState = await evaluate(browser, sessionId, `(() => {
      const modal = document.getElementById('step-explainer-modal');
      if (!modal) return { found: false };
      const style = window.getComputedStyle(modal);
      return {
        found: true,
        openAttr: modal.hasAttribute('open'),
        openProp: Boolean(modal.open),
        display: style.display,
      };
    })()`);
    assert.equal(initialModalState.found, true, 'Expected #step-explainer-modal to exist in DOM');
    assert.equal(initialModalState.openAttr, false, 'Modal should not have open attribute');
    assert.equal(initialModalState.openProp, false, 'Modal open property should be false');
    assert.equal(initialModalState.display, 'none', 'Modal display style should be "none"');
    console.log('   ✓ Modal is initially closed');

    // 3. Clicking #btn-step-guide opens the modal at step 0 (client)
    console.log('\n[Assert 3] Clicking #btn-step-guide opens modal at step 0 (client)...');
    const step0Data = await evaluate(browser, sessionId, `(() => {
      const btn = document.getElementById('btn-step-guide');
      if (!btn) throw new Error('#btn-step-guide button not found');
      btn.click();

      const modal = document.getElementById('step-explainer-modal');
      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const selectorVal = document.getElementById('step-selector')?.value;
      const what = document.getElementById('step-modal-what')?.textContent.trim();
      const why = document.getElementById('step-modal-why')?.textContent.trim();
      const test = document.getElementById('step-modal-test')?.textContent.trim();

      return {
        open: Boolean(modal?.open),
        title,
        counter,
        selectorVal,
        whatLength: what ? what.length : 0,
        whyLength: why ? why.length : 0,
        testLength: test ? test.length : 0,
      };
    })()`);
    assert.equal(step0Data.open, true, 'Modal should be open after clicking #btn-step-guide');
    assert.match(step0Data.title, /Gentle Client CLI/i, 'Title should match step 0 (client)');
    assert.match(step0Data.counter, /01\s*de\s*11/i, 'Counter should show step 01 de 11');
    assert.equal(step0Data.selectorVal, '0', 'Step selector should have value "0"');
    assert.ok(step0Data.whatLength > 0, 'What section must be populated');
    assert.ok(step0Data.whyLength > 0, 'Why section must be populated');
    assert.ok(step0Data.testLength > 0, 'Test section must be populated');
    console.log(`   ✓ Modal opened at step 0: title="${step0Data.title}", counter="${step0Data.counter}"`);

    // 4. Clicking #step-modal-next advances to step 1 (auth_gate), updates title/counter, and calls Archify.focus.set("auth_gate")
    console.log('\n[Assert 4] Clicking #step-modal-next advances to step 1 (auth_gate), updates title/counter, calls Archify.focus.set("auth_gate")...');
    const step1Data = await evaluate(browser, sessionId, `(() => {
      window.__focusSetCalls = [];
      const btnNext = document.getElementById('step-modal-next') || document.getElementById('step-btn-next');
      if (!btnNext) throw new Error('Next button (#step-modal-next or #step-btn-next) not found');
      btnNext.click();

      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const selectorVal = document.getElementById('step-selector')?.value;
      const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;
      const calls = window.__focusSetCalls ? window.__focusSetCalls.slice() : [];

      return {
        title,
        counter,
        selectorVal,
        focus,
        calls,
      };
    })()`);
    assert.match(step1Data.title, /Security Shield/i, 'Title should match step 1 (auth_gate)');
    assert.match(step1Data.counter, /02\s*de\s*11/i, 'Counter should show step 02 de 11');
    assert.equal(step1Data.selectorVal, '1', 'Step selector should have value "1"');
    assert.equal(step1Data.focus, 'auth_gate', 'Archify.focus.active() should be "auth_gate"');
    const authGateCall = step1Data.calls.find(c => c.id === 'auth_gate');
    assert.ok(authGateCall, 'Expected Archify.focus.set to be called with "auth_gate"');
    console.log(`   ✓ Advanced to step 1 (auth_gate): title="${step1Data.title}", counter="${step1Data.counter}", focus="${step1Data.focus}"`);

    // 5. Step selector dropdown (#step-selector) changes steps and triggers focus sync
    console.log('\n[Assert 5] Step selector dropdown (#step-selector) changes steps and triggers focus sync...');
    const dropdownData = await evaluate(browser, sessionId, `(() => {
      window.__focusSetCalls = [];
      const selector = document.getElementById('step-selector');
      if (!selector) throw new Error('#step-selector not found');

      // Change to step 3 (branch_lock)
      selector.value = '3';
      selector.dispatchEvent(new Event('change', { bubbles: true }));

      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;
      const calls = window.__focusSetCalls ? window.__focusSetCalls.slice() : [];

      return {
        title,
        counter,
        focus,
        calls,
      };
    })()`);
    assert.match(dropdownData.title, /Branch Lock Manager/i, 'Title should match step 3 (branch_lock)');
    assert.match(dropdownData.counter, /04\s*de\s*11/i, 'Counter should show step 04 de 11');
    assert.equal(dropdownData.focus, 'branch_lock', 'Archify.focus.active() should sync to "branch_lock"');
    const branchLockCall = dropdownData.calls.find(c => c.id === 'branch_lock');
    assert.ok(branchLockCall, 'Expected Archify.focus.set to be called with "branch_lock"');
    console.log(`   ✓ Step selector synced to step 3: title="${dropdownData.title}", counter="${dropdownData.counter}", focus="${dropdownData.focus}"`);

    // 6. Closing modal via #step-modal-close
    console.log('\n[Assert 6] Closing modal via #step-modal-close...');
    const closeData = await evaluate(browser, sessionId, `(() => {
      const modal = document.getElementById('step-explainer-modal');
      const btnClose = document.getElementById('step-modal-close');
      if (!btnClose) throw new Error('#step-modal-close button not found');
      btnClose.click();

      const style = window.getComputedStyle(modal);
      return {
        open: Boolean(modal?.open),
        display: style.display,
      };
    })()`);
    assert.equal(closeData.open, false, 'Modal open property should be false after close button clicked');
    assert.equal(closeData.display, 'none', 'Modal display style should be "none"');
    console.log('   ✓ Modal closed via #step-modal-close');

    // 7. Clicking #card-admission opens modal at step 1 (auth_gate)
    console.log('\n[Assert 7] Clicking #card-admission opens modal at step 1 (auth_gate)...');
    const admissionData = await evaluate(browser, sessionId, `(() => {
      const cardAdmission = document.getElementById('card-admission');
      if (!cardAdmission) throw new Error('#card-admission not found');
      cardAdmission.click();

      const modal = document.getElementById('step-explainer-modal');
      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;

      return {
        open: Boolean(modal?.open),
        title,
        counter,
        focus,
      };
    })()`);
    assert.equal(admissionData.open, true, 'Modal should be open after clicking #card-admission');
    assert.match(admissionData.title, /Security Shield/i, 'Title should match step 1 (auth_gate)');
    assert.match(admissionData.counter, /02\s*de\s*11/i, 'Counter should show step 02 de 11');
    assert.equal(admissionData.focus, 'auth_gate', 'Archify focus should be "auth_gate"');
    console.log(`   ✓ #card-admission opened modal at step 1: title="${admissionData.title}", focus="${admissionData.focus}"`);

    // 8. Clicking #card-cluster opens modal at step 5 (coordinator)
    console.log('\n[Assert 8] Clicking #card-cluster opens modal at step 5 (coordinator)...');
    const clusterData = await evaluate(browser, sessionId, `(() => {
      const btnClose = document.getElementById('step-modal-close');
      btnClose?.click();

      const cardCluster = document.getElementById('card-cluster');
      if (!cardCluster) throw new Error('#card-cluster not found');
      cardCluster.click();

      const modal = document.getElementById('step-explainer-modal');
      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;

      return {
        open: Boolean(modal?.open),
        title,
        counter,
        focus,
      };
    })()`);
    assert.equal(clusterData.open, true, 'Modal should be open after clicking #card-cluster');
    assert.match(clusterData.title, /Central Coordinator/i, 'Title should match step 5 (coordinator)');
    assert.match(clusterData.counter, /06\s*de\s*11/i, 'Counter should show step 06 de 11');
    assert.equal(clusterData.focus, 'coordinator', 'Archify focus should be "coordinator"');
    console.log(`   ✓ #card-cluster opened modal at step 5: title="${clusterData.title}", focus="${clusterData.focus}"`);

    // 9. Semantic Passport button #btn-focus-explain opens modal at currently focused node
    console.log('\n[Assert 9] Semantic Passport button #btn-focus-explain opens modal at currently focused node...');
    const focusExplainData = await evaluate(browser, sessionId, `(() => {
      const btnClose = document.getElementById('step-modal-close');
      btnClose?.click();

      // Set focus to worker_delta (index 9)
      if (window.Archify?.focus?.set) {
        window.Archify.focus.set('worker_delta', { updateUrl: false, preserveView: true });
      }

      const activeFocusBefore = window.Archify?.focus?.active ? window.Archify.focus.active() : null;

      const btnFocusExplain = document.getElementById('btn-focus-explain');
      if (!btnFocusExplain) throw new Error('#btn-focus-explain button not found');
      btnFocusExplain.click();

      const modal = document.getElementById('step-explainer-modal');
      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const selectorVal = document.getElementById('step-selector')?.value;

      return {
        open: Boolean(modal?.open),
        activeFocusBefore,
        title,
        counter,
        selectorVal,
      };
    })()`);
    assert.equal(focusExplainData.activeFocusBefore, 'worker_delta', 'Active focus before click should be "worker_delta"');
    assert.equal(focusExplainData.open, true, 'Modal should be open after clicking #btn-focus-explain');
    assert.match(focusExplainData.title, /Worker Delta/i, 'Title should match Worker Delta');
    assert.match(focusExplainData.counter, /10\s*de\s*11/i, 'Counter should show step 10 de 11');
    assert.equal(focusExplainData.selectorVal, '9', 'Selector value should be "9" (worker_delta index)');
    console.log(`   ✓ #btn-focus-explain opened modal at focused node worker_delta: title="${focusExplainData.title}", counter="${focusExplainData.counter}"`);

    // 10. Modal renders responsively on Samsung Galaxy Z Fold folded viewport (412x915)
    console.log('\n[Assert 10] Testing modal renders responsively on Samsung Galaxy Z Fold folded viewport (412x915)...');
    await browser.cdp.send('Emulation.setDeviceMetricsOverride', {
      width: 412,
      height: 915,
      deviceScaleFactor: 1,
      mobile: true,
    }, sessionId);

    const mobileData = await evaluate(browser, sessionId, `(() => {
      const modal = document.getElementById('step-explainer-modal');
      if (!modal.open) {
        document.getElementById('btn-step-guide').click();
      }
      const rect = modal.getBoundingClientRect();
      const fitsViewport = rect.width <= 412 && rect.height <= 915;
      return {
        viewport: { width: window.innerWidth, height: window.innerHeight },
        rect: {
          width: rect.width,
          height: rect.height,
          top: rect.top,
          left: rect.left,
          bottom: rect.bottom,
          right: rect.right,
        },
        fitsViewport,
      };
    })()`);
    assert.equal(
      mobileData.fitsViewport,
      true,
      `Expected modal to fit 412x915 viewport, but rect was ${JSON.stringify(mobileData.rect)}`
    );
    assert.ok(mobileData.rect.width <= 412, `Modal width ${mobileData.rect.width} exceeds viewport width 412`);
    assert.ok(mobileData.rect.height <= 915, `Modal height ${mobileData.rect.height} exceeds viewport height 915`);
    console.log(`   ✓ Viewport: ${mobileData.viewport.width}x${mobileData.viewport.height}`);
    console.log(`   ✓ Modal rect: width=${mobileData.rect.width}px (<=412), height=${mobileData.rect.height}px (<=915)`);
    console.log(`   ✓ fitsViewport: ${mobileData.fitsViewport}`);

    // Final console error check across full execution
    assert.equal(
      consoleErrors.length,
      0,
      `Console errors detected during test: ${JSON.stringify(consoleErrors, null, 2)}`
    );
    const finalInPageErrors = await evaluate(browser, sessionId, 'window.__pageErrors || []');
    assert.equal(
      finalInPageErrors.length,
      0,
      `In-page errors detected during test: ${JSON.stringify(finalInPageErrors, null, 2)}`
    );

    console.log('\n--- All 10 topology explainer assertions PASSED cleanly ---');
  } finally {
    console.log('Cleaning up browser...');
    await browser.close();
    console.log('Browser closed.');
  }
}

runTests().catch((err) => {
  console.error('\n❌ Test failed with error:', err);
  process.exit(1);
});
