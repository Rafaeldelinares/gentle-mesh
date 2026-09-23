import assert from 'node:assert/strict';
import { ChromeVisualBrowser } from '/home/rafael/.agents/skills/archify/bin/visual-check.mjs';

const CHROME_PATH = '/usr/bin/chromium';
const TARGET_URL = 'file:///home/rafael/proyectos/gentle-mesh/docs/architecture/gentle-mesh-test-verification-gates.html';

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
  console.log('--- Step Explainer Integration Test ---');
  console.log(`1. Launching ${CHROME_PATH} via ChromeVisualBrowser...`);
  const browser = new ChromeVisualBrowser(CHROME_PATH);

  try {
    const sessionId = await browser.sessionPromise;
    console.log(`   Attached to session: ${sessionId}`);

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

    // Install in-page unhandled error listener before document loads
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
      `,
    }, sessionId);

    console.log(`2. Navigating via CDP to ${TARGET_URL}...`);
    const loaded = browser.cdp.waitFor('Page.loadEventFired', sessionId);
    const navResult = await browser.cdp.send('Page.navigate', { url: TARGET_URL }, sessionId);
    if (navResult.errorText) {
      throw new Error(`Chrome navigation failed: ${navResult.errorText}`);
    }
    await loaded;
    console.log('   ✓ Page loaded successfully');

    console.log('3. Verifying 0 console errors or unhandled exceptions...');
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

    console.log('4. Testing opening modal via #btn-step-guide (STEPS button in toolbar)...');
    const modalData = await evaluate(browser, sessionId, `(() => {
      const btn = document.getElementById('btn-step-guide');
      if (!btn) throw new Error('#btn-step-guide button not found');
      btn.click();

      const modal = document.getElementById('step-explainer-modal');
      const title = document.getElementById('step-modal-title');
      const what = document.getElementById('step-modal-what');
      const why = document.getElementById('step-modal-why');
      const test = document.getElementById('step-modal-test');

      return {
        open: modal ? modal.open : false,
        title: title ? title.textContent.trim() : '',
        what: what ? what.textContent.trim() : '',
        why: why ? why.textContent.trim() : '',
        test: test ? test.textContent.trim() : '',
      };
    })()`);

    assert.equal(modalData.open, true, 'Expected #step-explainer-modal to be open');
    assert.ok(modalData.title.length > 0, 'Expected title to be populated');
    assert.ok(modalData.what.length > 0, 'Expected "what" field to be populated');
    assert.ok(modalData.why.length > 0, 'Expected "why" field to be populated');
    assert.ok(modalData.test.length > 0, 'Expected "test" field to be populated');
    console.log(`   ✓ Modal opened with title: "${modalData.title}"`);
    console.log(`   ✓ Fields populated (what: ${modalData.what.length} chars, why: ${modalData.why.length} chars, test: ${modalData.test.length} chars)`);

    console.log('5. Testing step navigation via #step-btn-next...');
    const navData = await evaluate(browser, sessionId, `(() => {
      const counterBefore = document.getElementById('step-modal-counter')?.textContent.trim();
      const focusBefore = window.Archify?.focus?.active ? window.Archify.focus.active() : null;

      const btnNext = document.getElementById('step-btn-next');
      if (!btnNext) throw new Error('#step-btn-next button not found');
      btnNext.click();

      const counterAfter = document.getElementById('step-modal-counter')?.textContent.trim();
      const focusAfter = window.Archify?.focus?.active ? window.Archify.focus.active() : null;
      const titleAfter = document.getElementById('step-modal-title')?.textContent.trim();

      return {
        counterBefore,
        focusBefore,
        counterAfter,
        focusAfter,
        titleAfter,
      };
    })()`);

    assert.notEqual(navData.counterBefore, navData.counterAfter, 'Counter should increment on next button click');
    assert.match(navData.counterAfter, /02/, `Expected step 02 in counter, got "${navData.counterAfter}"`);
    assert.equal(navData.focusAfter, 'gate_auth', `Expected Archify focus to update to "gate_auth", got "${navData.focusAfter}"`);
    console.log(`   ✓ Counter updated: "${navData.counterBefore}" -> "${navData.counterAfter}"`);
    console.log(`   ✓ Archify focus updated to: "${navData.focusAfter}" (Title: "${navData.titleAfter}")`);

    console.log('6. Testing closing and reopening modal via #card-admission (opens step 1 gate_auth)...');
    const admissionData = await evaluate(browser, sessionId, `(() => {
      const modal = document.getElementById('step-explainer-modal');
      const btnClose = document.getElementById('step-modal-close');
      if (!btnClose) throw new Error('#step-modal-close button not found');
      btnClose.click();
      const closed = !modal.open;

      const cardAdmission = document.getElementById('card-admission');
      if (!cardAdmission) throw new Error('#card-admission not found');
      cardAdmission.click();

      const reopened = modal.open === true;
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;

      return {
        closed,
        reopened,
        counter,
        title,
        focus,
      };
    })()`);

    assert.equal(admissionData.closed, true, 'Modal should close after clicking close button');
    assert.equal(admissionData.reopened, true, 'Modal should reopen after clicking #card-admission');
    assert.equal(admissionData.focus, 'gate_auth', `Expected focus to be "gate_auth", got "${admissionData.focus}"`);
    assert.match(admissionData.counter, /02/, `Expected step 02 for gate_auth, got "${admissionData.counter}"`);
    console.log(`   ✓ Modal closed and reopened via #card-admission (Focus: "${admissionData.focus}", Counter: "${admissionData.counter}")`);

    console.log('7. Testing closing and reopening modal via #card-runtime (opens step 4 gate_branch)...');
    const runtimeData = await evaluate(browser, sessionId, `(() => {
      const modal = document.getElementById('step-explainer-modal');
      const btnClose = document.getElementById('step-modal-close');
      if (!btnClose) throw new Error('#step-modal-close button not found');
      btnClose.click();
      const closed = !modal.open;

      const cardRuntime = document.getElementById('card-runtime');
      if (!cardRuntime) throw new Error('#card-runtime not found');
      cardRuntime.click();

      const reopened = modal.open === true;
      const counter = document.getElementById('step-modal-counter')?.textContent.trim();
      const title = document.getElementById('step-modal-title')?.textContent.trim();
      const focus = window.Archify?.focus?.active ? window.Archify.focus.active() : null;

      return {
        closed,
        reopened,
        counter,
        title,
        focus,
      };
    })()`);

    assert.equal(runtimeData.closed, true, 'Modal should close after clicking close button');
    assert.equal(runtimeData.reopened, true, 'Modal should reopen after clicking #card-runtime');
    assert.equal(runtimeData.focus, 'gate_branch', `Expected focus to be "gate_branch", got "${runtimeData.focus}"`);
    assert.match(runtimeData.counter, /05/, `Expected step 05 for gate_branch, got "${runtimeData.counter}"`);
    console.log(`   ✓ Modal closed and reopened via #card-runtime (Focus: "${runtimeData.focus}", Counter: "${runtimeData.counter}")`);

    console.log('8. Testing mobile viewport emulation at 412x915 (Samsung Galaxy Z Fold outer screen)...');
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
    console.log(`   ✓ Viewport: ${mobileData.viewport.width}x${mobileData.viewport.height}`);
    console.log(`   ✓ Modal rect: width=${mobileData.rect.width}px (<=412), height=${mobileData.rect.height}px (<=915)`);
    console.log(`   ✓ fitsViewport: ${mobileData.fitsViewport}`);

    // Final sanity check: no console errors throughout the entire test execution
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

    console.log('\n--- All step explainer tests PASSED cleanly ---');
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
